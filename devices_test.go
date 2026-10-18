package aqaramcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
)

// The header row and two devices as the live server lists them.
var (
	deviceHeader = []string{"endpoint id", "endpoint name", "device name", "device type", "position name", "position id"}
	lockRow      = []string{"Aqr~lock", "Дверной Замок", "Дверной замок", "DoorLock", "Прихожая", "Aqr~hall"}
	leakRow      = []string{"Aqr~leak", "Датчик Протечки", "Протечка ванная", "WaterLeakSensor", "Ванная", "Aqr~bath"}

	lock = Device{EndpointID: "Aqr~lock", EndpointName: "Дверной Замок", DeviceName: "Дверной замок", Type: "DoorLock", PositionName: "Прихожая", PositionID: "Aqr~hall"}
	leak = Device{EndpointID: "Aqr~leak", EndpointName: "Датчик Протечки", DeviceName: "Протечка ванная", Type: "WaterLeakSensor", PositionName: "Ванная", PositionID: "Aqr~bath"}
)

func TestClientDevices(t *testing.T) {
	tests := []struct {
		name     string
		filter   DeviceFilter
		reply    string
		want     []Device
		wantArgs string
		wantErr  string
	}{
		{
			name:     "reads the device table",
			reply:    tableReply("Device Base Inquiry successfully.", deviceHeader, lockRow, leakRow),
			want:     []Device{lock, leak},
			wantArgs: "{}", // an empty filter still sends the object the tool expects
		},
		{
			name:     "a filter travels as the tool's arguments",
			filter:   DeviceFilter{PositionIDs: []string{"Aqr~hall"}, DeviceTypes: []string{"DoorLock"}},
			reply:    tableReply("Device Base Inquiry successfully.", deviceHeader, lockRow),
			want:     []Device{lock},
			wantArgs: `{"position_ids":["Aqr~hall"],"device_types":["DoorLock"]}`,
		},
		{
			name: "columns are read by name, not by position",
			reply: tableReply("ok",
				[]string{"device type", "endpoint id", "position id", "position name", "device name", "endpoint name"},
				[]string{"DoorLock", "Aqr~lock", "Aqr~hall", "Прихожая", "Дверной замок", "Дверной Замок"}),
			want:     []Device{lock},
			wantArgs: "{}",
		},
		{
			name:     "an empty home is not an error",
			reply:    tableReply("Device Base Inquiry successfully.", deviceHeader),
			want:     []Device{},
			wantArgs: "{}",
		},
		{
			name:     "no table at all is not an error either",
			reply:    `{"content":[{"type":"text","text":"{\"message\":\"ok\",\"outputs\":[]}"}]}`,
			want:     []Device{},
			wantArgs: "{}",
		},
		{
			name:    "a refusal becomes a tool error",
			reply:   refusalReply("Device control action not supported"),
			wantErr: "aqaramcp: list devices: aqara tool device_base_inquiry: Device control action not supported",
		},
		{
			name:    "a refusal with a reason names it",
			reply:   toolResultJSON(`{"message":"Business error occurred.","trace_id":"trace","outputs":{"code":"ERROR.NoHomesUnderAccount","message":"No homes under account"},"panels":null}`, false),
			wantErr: "aqaramcp: list devices: aqara tool device_base_inquiry: Business error occurred: No homes under account (ERROR.NoHomesUnderAccount)",
		},
		{
			name:    "an MCP-level tool failure is a tool error too",
			reply:   `{"content":[{"type":"text","text":"{\"message\":\"Internal error\",\"outputs\":{}}"}],"isError":true}`,
			wantErr: "aqaramcp: list devices: aqara tool device_base_inquiry: Internal error",
		},
		{
			name:    "a table without the expected columns is refused",
			reply:   tableReply("ok", []string{"id", "name"}, []string{"1", "x"}),
			wantErr: `aqaramcp: list devices: device_base_inquiry: answer has no "endpoint id" column (columns: id, name)`,
		},
		{
			name:    "prose instead of the envelope is refused",
			reply:   `{"content":[{"type":"text","text":"Here are your devices: a lock and a sensor."}]}`,
			wantErr: "aqaramcp: list devices: device_base_inquiry: answer is not the JSON envelope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newServer(t, &server{reply: constant(tt.reply)})

			got, err := c.Devices(context.Background(), tt.filter)
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Devices: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("devices = %+v, want %+v", got, tt.want)
			}
			if last := s.lastCall(); last.tool != toolDeviceBase || last.args != tt.wantArgs {
				t.Errorf("call = %+v, want tool %q args %q", last, toolDeviceBase, tt.wantArgs)
			}
		})
	}
}

func TestClientStatuses(t *testing.T) {
	statusHeader := append(append([]string{}, deviceHeader...), "status")

	tests := []struct {
		name     string
		filter   StatusFilter
		reply    string
		want     []DeviceStatus
		wantArgs string
		wantErr  string
	}{
		{
			name: "reads every device's state in one call",
			reply: tableReply("Device status inquiry successfully.", statusHeader,
				append(append([]string{}, lockRow...), "{'lock_state': '1', 'online_offline': 'online'}"),
				append(append([]string{}, leakRow...), "{'water_leak': 'False', 'online_offline': 'online'}")),
			want: []DeviceStatus{
				{Device: lock, Status: Status{"lock_state": "1", "online_offline": "online"}},
				{Device: leak, Status: Status{"water_leak": "False", "online_offline": "online"}},
			},
			wantArgs: "{}",
		},
		{
			name:   "a filter travels as the tool's arguments",
			filter: StatusFilter{DeviceIDs: []string{"Aqr~lock"}},
			reply: tableReply("Device status inquiry successfully.", statusHeader,
				append(append([]string{}, lockRow...), "{'lock_state': '1', 'online_offline': 'online'}")),
			want: []DeviceStatus{
				{Device: lock, Status: Status{"lock_state": "1", "online_offline": "online"}},
			},
			wantArgs: `{"device_ids":["Aqr~lock"]}`,
		},
		{
			name:    "an unreadable state names the device",
			reply:   tableReply("ok", statusHeader, append(append([]string{}, lockRow...), "locked")),
			wantErr: `aqaramcp: read statuses: device Aqr~lock: status "locked": not a dictionary`,
		},
		{
			name:    "a refusal becomes a tool error",
			reply:   refusalReply("No log found for device control"),
			wantErr: "aqaramcp: read statuses: aqara tool device_status_inquiry: No log found for device control",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newServer(t, &server{reply: constant(tt.reply)})

			got, err := c.Statuses(context.Background(), tt.filter)
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				var toolErr *ToolError
				if errors.As(err, &toolErr) && toolErr.Tool != toolDeviceStatus {
					t.Errorf("tool error names %q, want %q", toolErr.Tool, toolDeviceStatus)
				}
				return
			}
			if err != nil {
				t.Fatalf("Statuses: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("statuses = %+v, want %+v", got, tt.want)
			}
			if last := s.lastCall(); last.tool != toolDeviceStatus || last.args != tt.wantArgs {
				t.Errorf("call = %+v, want tool %q args %q", last, toolDeviceStatus, tt.wantArgs)
			}
		})
	}
}

func TestDeviceFingerprint(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z]+-[0-9a-f]{8}$`)
	// lockAgain is the lock as a later sign-in lists it: new IDs, the endpoint label in another language.
	lockAgain := lock
	lockAgain.EndpointID, lockAgain.EndpointName, lockAgain.PositionID = "Aqr~lock2", "Door Lock", "Aqr~hall2"
	renamed, moved, retyped := lock, lock, lock
	renamed.DeviceName = "Замок"
	moved.PositionName = "Кухня"
	retyped.Type = "Outlet"

	tests := []struct {
		name     string
		device   Device
		other    Device
		want     string // the device's fingerprint; empty to check only the comparison
		wantSame bool
	}{
		{
			name:     "only type, name and room count",
			device:   lock,
			other:    lockAgain,
			wantSame: true,
		},
		{
			name:   "a different name changes it",
			device: lock,
			other:  renamed,
		},
		{
			name:   "a different room changes it",
			device: lock,
			other:  moved,
		},
		{
			name:   "a different type changes it",
			device: lock,
			other:  retyped,
		},
		{
			name:   "the scheme is pinned",
			device: Device{Type: "DoorLock", DeviceName: "Дверной замок", PositionName: "Прихожая"},
			other:  leak,
			want:   "doorlock-3b7f29e4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, other := tt.device.Fingerprint(), tt.other.Fingerprint()
			for _, fp := range []string{got, other} {
				if !shape.MatchString(fp) {
					t.Errorf("fingerprint %q does not match %s", fp, shape)
				}
			}
			if tt.want != "" && got != tt.want {
				t.Errorf("fingerprint = %q, want %q", got, tt.want)
			}
			if same := got == other; same != tt.wantSame {
				t.Errorf("%q vs %q: same = %t, want %t", got, other, same, tt.wantSame)
			}
		})
	}
}

// tableReply renders a successful tool answer the way the live server does:
// the envelope as JSON in the text content, with the table in outputs.
func tableReply(message string, rows ...[]string) string {
	envelope, err := json.Marshal(map[string]any{
		"message":  message,
		"trace_id": "trace",
		"outputs":  rows,
		"panels":   map[string]any{},
	})
	if err != nil {
		panic(err)
	}
	return toolResultJSON(string(envelope), false)
}

// refusalReply renders the answer to a query the platform would not run.
func refusalReply(message string) string {
	return toolResultJSON(`{"message":"`+message+`","trace_id":"trace","outputs":{},"panels":null}`, false)
}

// toolResultJSON wraps envelope JSON in an MCP tool result.
func toolResultJSON(text string, isError bool) string {
	result, err := json.Marshal(ToolResult{Content: []Content{{Type: "text", Text: text}}, IsError: isError})
	if err != nil {
		panic(err)
	}
	return string(result)
}
