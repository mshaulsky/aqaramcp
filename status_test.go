package aqaramcp

import (
	"reflect"
	"testing"
)

func TestParseStatus(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    Status
		wantErr string
	}{
		{
			name: "reads the platform's dictionary literal",
			text: "{'water_leak': 'False', 'online_offline': 'online'}",
			want: Status{"water_leak": "False", "online_offline": "online"},
		},
		{
			name: "an empty dictionary is an empty status",
			text: "{}",
			want: Status{},
		},
		{
			name: "double quotes and odd spacing are fine",
			text: `{ "on_off" : "on" ,'x':'y' }`,
			want: Status{"on_off": "on", "x": "y"},
		},
		{
			name: "bare tokens keep their text",
			text: "{'level': 42, 'flag': True, 'nothing': None}",
			want: Status{"level": "42", "flag": "True", "nothing": "None"},
		},
		{
			name: "escapes are honoured",
			text: `{'name': 'it\'s', 'path': 'a\\b'}`,
			want: Status{"name": "it's", "path": `a\b`},
		},
		{
			name: "a nested dictionary is kept whole as text",
			text: "{'color': {'r': 255, 'g': 0}, 'on_off': 'on'}",
			want: Status{"color": "{'r': 255, 'g': 0}", "on_off": "on"},
		},
		{
			name: "a nested list with brackets inside strings is kept whole",
			text: `{'modes': ['a,b', "c]d", {'x': [1, 2]}], 'k': 'v'}`,
			want: Status{"modes": `['a,b', "c]d", {'x': [1, 2]}]`, "k": "v"},
		},
		{
			name: "carriage returns are whitespace",
			text: "{'a':\r\n'1',\r\n'b': '2'}",
			want: Status{"a": "1", "b": "2"},
		},
		{
			name:    "not a dictionary",
			text:    "locked",
			wantErr: `status "locked": not a dictionary`,
		},
		{
			name:    "a key must be quoted",
			text:    "{on_off: 'on'}",
			wantErr: "key: expected a quoted string",
		},
		{
			name:    "a key needs a value",
			text:    "{'on_off' 'on'}",
			wantErr: `expected ':' after "on_off"`,
		},
		{
			name:    "an unterminated string is refused",
			text:    "{'on_off': 'on}",
			wantErr: "unterminated string",
		},
		{
			name:    "an unterminated nested literal is refused",
			text:    "{'color': {'r': 1, 'k': 'v'}",
			wantErr: `value of "color": unterminated {`,
		},
		{
			name:    "an empty value is refused",
			text:    "{'a': , 'b': '2'}",
			wantErr: `value of "a": missing`,
		},
		{
			name:    "pairs must be separated by commas",
			text:    "{'a': '1' 'b': '2'}",
			wantErr: `expected ',' after "a"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStatus(tt.text)
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("parseStatus: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("status = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusGet(t *testing.T) {
	status := Status{"lock_state": "1"}

	tests := []struct {
		name      string
		key       string
		want      string
		wantFound bool
	}{
		{name: "finds a key", key: "lock_state", want: "1", wantFound: true},
		{name: "reports a missing key", key: "water_leak"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := status.Get(tt.key)
			if got != tt.want || found != tt.wantFound {
				t.Errorf("Get = %q, %t; want %q, %t", got, found, tt.want, tt.wantFound)
			}
		})
	}
}

func TestStatusOnline(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		want   bool
	}{
		{name: "online", status: Status{KeyOnline: "online"}, want: true},
		{name: "offline", status: Status{KeyOnline: "offline"}},
		{name: "unknown counts as offline", status: Status{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.Online(); got != tt.want {
				t.Errorf("Online = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestStatusBool(t *testing.T) {
	tests := []struct {
		name    string
		status  Status
		key     string
		want    bool
		wantErr string
	}{
		{name: "a Python True", status: Status{"water_leak": "True"}, key: "water_leak", want: true},
		{name: "a Python False", status: Status{"water_leak": "False"}, key: "water_leak"},
		{name: "on", status: Status{"on_off": "on"}, key: "on_off", want: true},
		{name: "off", status: Status{"on_off": "off"}, key: "on_off"},
		{name: "one", status: Status{"lock_state": "1"}, key: "lock_state", want: true},
		{name: "zero", status: Status{"lock_state": "0"}, key: "lock_state"},
		{name: "a missing key", status: Status{}, key: "on_off", wantErr: `status has no "on_off"`},
		{name: "text is not a flag", status: Status{"mode": "auto"}, key: "mode", wantErr: `status mode="auto": not a flag`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.status.Bool(tt.key)
			if tt.wantErr != "" {
				errContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Bool: %v", err)
			}
			if got != tt.want {
				t.Errorf("Bool = %t, want %t", got, tt.want)
			}
		})
	}
}
