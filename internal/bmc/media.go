package bmc

// MediaState is the lifecycle of a virtual media mount.
type MediaState string

// Media states reported to the UI.
const (
	MediaNone     MediaState = "none"
	MediaMounting MediaState = "mounting"
	MediaMounted  MediaState = "mounted"
)

// MediaStatus describes the virtual CD for the UI.
type MediaStatus struct {
	State       MediaState `json:"state"`
	Image       string     `json:"image,omitempty"` // file name only
	BytesServed int64      `json:"bytes_served"`
	Error       string     `json:"error,omitempty"` // why the last mount ended, if it failed
}

// VirtualMedia is implemented by consoles that can present a local disc image to the host.
type VirtualMedia interface {
	// MountISO attaches the image at the absolute path as a read-only USB CD-ROM.
	MountISO(path string) error
	// UnmountISO ejects the image.
	UnmountISO() error
	MediaStatus() MediaStatus
}
