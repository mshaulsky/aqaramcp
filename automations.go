package aqaramcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Home is one home of the account, the top of the position tree.
type Home struct {
	ID   string // valid while the key is: a new sign-in mints a new one
	Name string // the name given in the app
}

// AutomationFilter narrows an automation query. The platform refuses an
// empty one, so name at least a home, a device or an automation.
type AutomationFilter struct {
	AutomationIDs []string // automation IDs, as in Automation.ID
	PositionIDs   []string // home or room IDs, as in Home.ID
	DeviceIDs     []string // endpoint IDs, as in Device.EndpointID
}

// Automation is one automation set up in the app.
type Automation struct {
	ID           string // valid while the key is, like every ID
	Name         string // the name the app shows, which spells out trigger and action
	PositionName string // the home or room it belongs to
}

// AutomationRun is one execution of an automation.
type AutomationRun struct {
	Automation   string    // the automation's name; the history carries no ID
	PositionName string    // the home or room it belongs to
	At           time.Time // to the minute, in the zone of the queried range's start
	Failed       bool      // the automation ran but its action failed
}

const (
	toolHomes             = "all_homes_inquiry"
	toolAutomations       = "automation_base_inquiry"
	toolAutomationHistory = "automation_execution_history_inquiry"

	colHomeName       = "home name"
	colHomeID         = "home id"
	colAutomationID   = "automation id"
	colAutomationName = "automation name"

	// rangeLayout is how the history tool takes its time range.
	rangeLayout = "2006-01-02 15:04:05"
	// runLayout is how the history tool reports a run: wall-clock minutes, no zone.
	runLayout = "2006-01-02 15:04"
	// noHistory is the message of an empty history, which the platform words as a failure.
	noHistory = "No automation execution history log found"
)

// defaultHistoryZone is the clock the history tool keeps, whatever the home's
// region: UTC+8 as observed on the RU server; WithHistoryZone overrides it.
var defaultHistoryZone = time.FixedZone("UTC+8", 8*60*60)

// automationArgs are the arguments the automation tools take.
type automationArgs struct {
	TimeRange     []string `json:"time_range,omitempty"`
	AutomationIDs []string `json:"automation_ids,omitempty"`
	PositionIDs   []string `json:"position_ids,omitempty"`
	DeviceIDs     []string `json:"device_ids,omitempty"`
}

// history is what the history tool holds in outputs on success.
type history struct {
	Data *[]struct {
		PositionName   string `json:"position_name"`
		AutomationName string `json:"automation_name"`
		Logs           struct {
			Success struct {
				Times []string `json:"execute_time"`
			} `json:"success"`
			Failed struct {
				Times []string `json:"execute_time"`
			} `json:"failed"`
		} `json:"execute_logs"`
	} `json:"data"`
}

// Homes lists the homes of the account.
func (c *Client) Homes(ctx context.Context) ([]Home, error) {
	t, err := c.table(ctx, toolHomes, struct{}{}, colHomeName, colHomeID)
	if err != nil {
		return nil, fmt.Errorf("aqaramcp: list homes: %w", err)
	}
	homes := make([]Home, 0, len(t.rows))
	for _, row := range t.rows {
		homes = append(homes, Home{ID: t.get(row, colHomeID), Name: t.get(row, colHomeName)})
	}
	return homes, nil
}

// Automations lists the automations the filter selects. The platform refuses
// an empty filter; a home ID lists every automation of the home.
func (c *Client) Automations(ctx context.Context, filter AutomationFilter) ([]Automation, error) {
	t, err := c.table(ctx, toolAutomations, filter.args(),
		colAutomationID, colAutomationName, colPositionName)
	if err != nil {
		return nil, fmt.Errorf("aqaramcp: list automations: %w", err)
	}
	automations := make([]Automation, 0, len(t.rows))
	for _, row := range t.rows {
		automations = append(automations, Automation{
			ID:           t.get(row, colAutomationID),
			Name:         t.get(row, colAutomationName),
			PositionName: t.get(row, colPositionName),
		})
	}
	return automations, nil
}

// AutomationRuns reads which automations ran between from and to, one run
// per execution. It is the only event log the server offers, at minute
// resolution. The platform keeps it on its own clock (see WithHistoryZone),
// so from and to may be in any zone and the runs come back in from's. An
// empty history is nil without error; the platform refuses an empty filter.
func (c *Client) AutomationRuns(
	ctx context.Context,
	from, to time.Time,
	filter AutomationFilter,
) ([]AutomationRun, error) {
	args := filter.args()
	args.TimeRange = []string{from.In(c.zone).Format(rangeLayout), to.In(c.zone).Format(rangeLayout)}
	env, err := c.answer(ctx, toolAutomationHistory, args)
	if err != nil {
		return nil, fmt.Errorf("aqaramcp: read automation history: %w", err)
	}
	if strings.Contains(env.Message, noHistory) {
		return nil, nil
	}
	var h history
	if json.Unmarshal(env.Outputs, &h) != nil || h.Data == nil {
		return nil, fmt.Errorf("aqaramcp: read automation history: %w", env.refusal(toolAutomationHistory))
	}

	var runs []AutomationRun
	for _, entry := range *h.Data {
		for _, outcome := range []struct {
			times  []string
			failed bool
		}{
			{times: entry.Logs.Success.Times},
			{times: entry.Logs.Failed.Times, failed: true},
		} {
			for _, s := range outcome.times {
				at, err := time.ParseInLocation(runLayout, s, c.zone)
				if err != nil {
					return nil, fmt.Errorf("aqaramcp: read automation history: %s: %w", entry.AutomationName, err)
				}
				runs = append(runs, AutomationRun{
					Automation:   entry.AutomationName,
					PositionName: entry.PositionName,
					At:           at.In(from.Location()),
					Failed:       outcome.failed,
				})
			}
		}
	}
	return runs, nil
}

// args turns the filter into the tool's arguments.
func (f AutomationFilter) args() automationArgs {
	return automationArgs{AutomationIDs: f.AutomationIDs, PositionIDs: f.PositionIDs, DeviceIDs: f.DeviceIDs}
}
