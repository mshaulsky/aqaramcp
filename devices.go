package aqaramcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Device is one device of the account as the platform lists it. Endpoints
// are the platform's unit — a multi-gang switch is several — and the
// endpoint ID is the key every other call takes.
type Device struct {
	EndpointID   string // the ID to query by
	EndpointName string // the platform's label for the endpoint, localised
	DeviceName   string // the name given in the app
	Type         string // e.g. DoorLock; see the Type constants
	PositionName string // the room, as named in the app
	PositionID   string
}

// DeviceStatus is a device together with its current state.
type DeviceStatus struct {
	Device
	Status Status
}

// DeviceFilter narrows a device listing. Empty fields match everything.
type DeviceFilter struct {
	PositionIDs []string
	DeviceTypes []string
}

// StatusFilter narrows a status query. Empty fields match everything, which
// is the cheapest way to poll a whole home: one call, every device.
type StatusFilter struct {
	DeviceIDs   []string // endpoint IDs, as in Device.EndpointID
	PositionIDs []string
	DeviceTypes []string
}

// Device types as the platform names them, for filters and for telling
// devices apart. The list the platform documents is longer; these are the
// ones a home dashboard meets.
const (
	TypeHub               = "Hub"
	TypeDoorLock          = "DoorLock"
	TypeWaterLeakSensor   = "WaterLeakSensor"
	TypeOutlet            = "Outlet"
	TypeButton            = "Button"
	TypeSwitch            = "Switch"
	TypeLight             = "Light"
	TypeTemperatureSensor = "TemperatureSensor"
	TypeHumiditySensor    = "HumiditySensor"
	TypeDoorSensor        = "DoorSensor"
	TypeMotionSensor      = "MotionSensor"
)

const (
	toolDeviceBase   = "device_base_inquiry"
	toolDeviceStatus = "device_status_inquiry"

	// Column headers of the tables the tools answer with.
	colEndpointID   = "endpoint id"
	colEndpointName = "endpoint name"
	colDeviceName   = "device name"
	colDeviceType   = "device type"
	colPositionName = "position name"
	colPositionID   = "position id"
	colStatus       = "status"
)

// deviceColumns are the columns every device table carries.
var deviceColumns = []string{colEndpointID, colEndpointName, colDeviceName, colDeviceType, colPositionName, colPositionID}

// deviceArgs are the arguments the device tools take; all are optional.
type deviceArgs struct {
	DeviceIDs   []string `json:"device_ids,omitempty"`
	PositionIDs []string `json:"position_ids,omitempty"`
	DeviceTypes []string `json:"device_types,omitempty"`
}

// envelope is what every Aqara tool answers with, as JSON in the text
// content. On success outputs is a table; on refusal it is an empty object
// and message says why.
type envelope struct {
	Message string          `json:"message"`
	TraceID string          `json:"trace_id"`
	Outputs json.RawMessage `json:"outputs"`
}

// table is a tool's answer with its header row turned into a column index,
// so rows are read by column name rather than by position.
type table struct {
	columns map[string]int
	rows    [][]string
}

// Devices lists the devices of the account. It is the discovery call: run
// it once to learn the IDs and types, then poll [Client.Statuses].
func (c *Client) Devices(ctx context.Context, filter DeviceFilter) ([]Device, error) {
	args := deviceArgs{PositionIDs: filter.PositionIDs, DeviceTypes: filter.DeviceTypes}
	t, err := c.table(ctx, toolDeviceBase, args, deviceColumns...)
	if err != nil {
		return nil, fmt.Errorf("aqaramcp: list devices: %w", err)
	}
	devices := make([]Device, 0, len(t.rows))
	for _, row := range t.rows {
		devices = append(devices, t.device(row))
	}
	return devices, nil
}

// Statuses reads the current state of devices. With an empty filter it
// returns every device in one call, which is the call a polling loop
// repeats.
func (c *Client) Statuses(ctx context.Context, filter StatusFilter) ([]DeviceStatus, error) {
	// The filter is the tool's argument set exactly; the conversion is
	// checked field by field at compile time.
	t, err := c.table(ctx, toolDeviceStatus, deviceArgs(filter), append(deviceColumns, colStatus)...)
	if err != nil {
		return nil, fmt.Errorf("aqaramcp: read statuses: %w", err)
	}
	statuses := make([]DeviceStatus, 0, len(t.rows))
	for _, row := range t.rows {
		device := t.device(row)
		status, err := parseStatus(t.get(row, colStatus))
		if err != nil {
			return nil, fmt.Errorf("aqaramcp: read statuses: device %s: %w", device.EndpointID, err)
		}
		statuses = append(statuses, DeviceStatus{Device: device, Status: status})
	}
	return statuses, nil
}

// table runs a tool and reads its answer as a table with at least the given
// columns.
func (c *Client) table(ctx context.Context, tool string, args any, want ...string) (table, error) {
	result, err := c.call(ctx, tool, args)
	if err != nil {
		return table{}, err
	}
	text := result.Text()
	var env envelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return table{}, fmt.Errorf("%s: answer is not the JSON envelope: %w: %s", tool, err, truncate([]byte(text)))
	}
	var rows [][]string
	if result.IsError || json.Unmarshal(env.Outputs, &rows) != nil {
		// The platform reports a refusal as a message with an empty object
		// where the table would be, not as an MCP-level error.
		return table{}, &ToolError{Tool: tool, Message: env.Message, TraceID: env.TraceID}
	}
	if len(rows) == 0 {
		return table{}, nil
	}

	t := table{columns: make(map[string]int, len(rows[0])), rows: rows[1:]}
	for i, name := range rows[0] {
		t.columns[name] = i
	}
	for _, name := range want {
		if _, ok := t.columns[name]; !ok {
			return table{}, fmt.Errorf("%s: answer has no %q column (columns: %s)", tool, name, strings.Join(rows[0], ", "))
		}
	}
	return t, nil
}

// call runs one tool without the package prefix on its errors; the exported
// method that started the operation adds it.
func (c *Client) call(ctx context.Context, tool string, args any) (ToolResult, error) {
	var result ToolResult
	if err := c.request(ctx, "tools/call", callParams{Name: tool, Arguments: args}, &result); err != nil {
		return ToolResult{}, fmt.Errorf("call %s: %w", tool, err)
	}
	return result, nil
}

// device reads the device columns of one row.
func (t table) device(row []string) Device {
	return Device{
		EndpointID:   t.get(row, colEndpointID),
		EndpointName: t.get(row, colEndpointName),
		DeviceName:   t.get(row, colDeviceName),
		Type:         t.get(row, colDeviceType),
		PositionName: t.get(row, colPositionName),
		PositionID:   t.get(row, colPositionID),
	}
}

// get returns one cell by column name; a short row reads as empty.
func (t table) get(row []string, column string) string {
	i, ok := t.columns[column]
	if !ok || i >= len(row) {
		return ""
	}
	return row[i]
}
