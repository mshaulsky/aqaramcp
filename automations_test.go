package aqaramcp

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// historyReply renders a history answer the way the live server does, outputs being an object.
func historyReply(outputs string) string {
	return toolResultJSON(`{"message":"Automation execution history inquiry successfully.","trace_id":"trace",`+
		`"outputs":`+outputs+`,"panels":null}`, false)
}

func TestClientHomes(t *testing.T) {
	tests := []struct {
		name    string
		reply   string
		want    []Home
		wantErr string
	}{
		{
			name: "reads the home table",
			reply: tableReply("All homes inquiry successfully.",
				[]string{"home name", "home id"},
				[]string{"Мой Дом", "Aqr~home"}),
			want: []Home{{ID: "Aqr~home", Name: "Мой Дом"}},
		},
		{
			name:    "a refusal becomes a tool error",
			reply:   refusalReply("All homes inquiry failed."),
			wantErr: "aqaramcp: list homes: aqara tool all_homes_inquiry: All homes inquiry failed.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newServer(t, &server{reply: constant(tt.reply)})

			got, err := c.Homes(context.Background())
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Homes: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("homes = %+v, want %+v", got, tt.want)
			}
			if last := s.lastCall(); last.tool != toolHomes || last.args != "{}" {
				t.Errorf("call = %+v, want tool %q args {}", last, toolHomes)
			}
		})
	}
}

func TestClientAutomations(t *testing.T) {
	header := []string{"automation id", "automation name", "position name"}

	tests := []struct {
		name     string
		filter   AutomationFilter
		reply    string
		want     []Automation
		wantArgs string
		wantErr  string
	}{
		{
			name:   "reads the automations of a home",
			filter: AutomationFilter{PositionIDs: []string{"Aqr~home"}},
			reply: tableReply("Automation base inquiry successfully.", header,
				[]string{"Aqr~auto", "КОГДА Разблокировать ТОГДА Push-уведомление", "Мой Дом"}),
			want: []Automation{
				{
					ID:           "Aqr~auto",
					Name:         "КОГДА Разблокировать ТОГДА Push-уведомление",
					PositionName: "Мой Дом",
				},
			},
			wantArgs: `{"position_ids":["Aqr~home"]}`,
		},
		{
			name:  "an empty filter is refused by the platform",
			reply: refusalReply("Automation base inquiry failed."),
			wantErr: "aqaramcp: list automations: aqara tool automation_base_inquiry: " +
				"Automation base inquiry failed.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newServer(t, &server{reply: constant(tt.reply)})

			got, err := c.Automations(context.Background(), tt.filter)
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Automations: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("automations = %+v, want %+v", got, tt.want)
			}
			if last := s.lastCall(); last.tool != toolAutomations || last.args != tt.wantArgs {
				t.Errorf("call = %+v, want tool %q args %q", last, toolAutomations, tt.wantArgs)
			}
		})
	}
}

func TestClientAutomationRuns(t *testing.T) {
	moscow := time.FixedZone("MSK", 3*60*60)
	dayArgs := `{"time_range":["2026-10-25 00:00:00","2026-10-25 23:59:59"],"position_ids":["Aqr~home"]}`
	const home = "Мой Дом"
	const lockName = "КОГДА Однократное нажатие ТОГДА Умный дверной замок U200 Заблокировать"

	tests := []struct {
		name     string
		zone     *time.Location
		history  *time.Location // the clock the history is kept in; nil is the default
		reply    string
		want     []AutomationRun
		wantArgs string
		wantErr  string
	}{
		{
			name: "one run per execution time, successes and failures",
			zone: defaultHistoryZone,
			reply: historyReply(`{"data":[{"position_name":"Мой Дом","execute_logs":` +
				`{"failed":{"execute_count":1,"execute_time":["2026-10-25 09:00"]},` +
				`"success":{"execute_count":2,"execute_time":["2026-10-25 18:38","2026-10-25 17:34"]}},` +
				`"automation_name":"` + lockName + `"}],"time_range":["2026-10-25 00:00","2026-10-25 23:59"]}`),
			want: []AutomationRun{
				{Automation: lockName, PositionName: home, At: time.Date(2026, 10, 25, 18, 38, 0, 0, defaultHistoryZone)},
				{Automation: lockName, PositionName: home, At: time.Date(2026, 10, 25, 17, 34, 0, 0, defaultHistoryZone)},
				{
					Automation:   lockName,
					PositionName: "Мой Дом",
					At:           time.Date(2026, 10, 25, 9, 0, 0, 0, defaultHistoryZone),
					Failed:       true,
				},
			},
			wantArgs: dayArgs,
		},
		{
			name: "the platform's clock is UTC+8: the range is converted, the runs come back in from's zone",
			zone: moscow,
			reply: historyReply(`{"data":[{"position_name":"Мой Дом","execute_logs":` +
				`{"failed":{"execute_count":0,"execute_time":[]},` +
				`"success":{"execute_count":1,"execute_time":["2026-10-25 02:23"]}},` +
				`"automation_name":"` + lockName + `"}]}`),
			want: []AutomationRun{
				{Automation: lockName, PositionName: home, At: time.Date(2026, 10, 24, 21, 23, 0, 0, moscow)},
			},
			wantArgs: `{"time_range":["2026-10-25 05:00:00","2026-10-26 04:59:59"],"position_ids":["Aqr~home"]}`,
		},
		{
			name:    "WithHistoryZone moves the platform's clock",
			zone:    moscow,
			history: moscow,
			reply: historyReply(`{"data":[{"position_name":"Мой Дом","execute_logs":` +
				`{"failed":{"execute_count":0,"execute_time":[]},` +
				`"success":{"execute_count":1,"execute_time":["2026-10-25 02:23"]}},` +
				`"automation_name":"` + lockName + `"}]}`),
			want: []AutomationRun{
				{Automation: lockName, PositionName: home, At: time.Date(2026, 10, 25, 2, 23, 0, 0, moscow)},
			},
			wantArgs: dayArgs,
		},
		{
			name: "an empty history is no runs, not an error",
			zone: defaultHistoryZone,
			reply: toolResultJSON(`{"message":"Automation execution history inquiry failed: `+
				`No automation execution history log found.","outputs":{}}`, false),
			wantArgs: dayArgs,
		},
		{
			name:  "a refusal becomes a tool error",
			zone:  defaultHistoryZone,
			reply: refusalReply("Automation execution history inquiry failed."),
			wantErr: "aqaramcp: read automation history: aqara tool automation_execution_history_inquiry: " +
				"Automation execution history inquiry failed.",
		},
		{
			name: "an unreadable time is refused",
			zone: defaultHistoryZone,
			reply: historyReply(
				`{"data":[{"automation_name":"x","execute_logs":{"success":{"execute_time":["18:38"]}}}]}`),
			wantErr: `aqaramcp: read automation history: x: parsing time "18:38"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []Option
			if tt.history != nil {
				opts = append(opts, WithHistoryZone(tt.history))
			}
			c, s := newServer(t, &server{reply: constant(tt.reply)}, opts...)
			from := time.Date(2026, 10, 25, 0, 0, 0, 0, tt.zone)
			to := time.Date(2026, 10, 25, 23, 59, 59, 0, tt.zone)

			filter := AutomationFilter{PositionIDs: []string{"Aqr~home"}}

			got, err := c.AutomationRuns(context.Background(), from, to, filter)
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("AutomationRuns: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("runs = %+v, want %+v", got, tt.want)
			}
			if last := s.lastCall(); last.tool != toolAutomationHistory || last.args != tt.wantArgs {
				t.Errorf("call = %+v, want tool %q args %q", last, toolAutomationHistory, tt.wantArgs)
			}
		})
	}
}
