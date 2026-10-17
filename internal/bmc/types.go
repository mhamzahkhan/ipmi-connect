package bmc

// TargetType identifies the BMC vendor
type TargetType string

const (
	TargetAuto       TargetType = "auto"
	TargetDell       TargetType = "dell"
	TargetSupermicro TargetType = "supermicro"
)

// SessionState holds status and telemetry of an active BMC session
type SessionState struct {
	Host          string     `json:"host"`
	TargetType    TargetType `json:"target_type"`
	Model         string     `json:"model"`
	Firmware      string     `json:"firmware"`
	Connected     bool       `json:"connected"`
	Authenticated bool       `json:"authenticated"`
	PowerState    string     `json:"power_state"`
	ScreenWidth   int        `json:"screen_width"`
	ScreenHeight  int        `json:"screen_height"`
	FPS           int        `json:"fps"`
	StatusText    string     `json:"status_text"`
	VirtualMedia  string     `json:"virtual_media"`
}

// PowerAction represents a chassis power command
type PowerAction string

const (
	PowerOn      PowerAction = "power_on"
	PowerOff     PowerAction = "power_off"
	PowerReset   PowerAction = "power_reset"
	PowerCycle   PowerAction = "power_cycle"
	PowerSoftOff PowerAction = "power_soft_off"
)
