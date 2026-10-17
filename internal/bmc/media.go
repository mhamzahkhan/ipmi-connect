package bmc

// MediaTarget is a virtual drive on the host.
type MediaTarget string

// Virtual drive targets.
const (
	TargetCD        MediaTarget = "cd"        // ISO images, read-only
	TargetRemovable MediaTarget = "removable" // IMG images of any size
	TargetFloppy    MediaTarget = "floppy"    // small IMG images
)

// MediaImage is a disc or disk image the user has added.
type MediaImage struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Size     int64       `json:"size"`
	Kind     string      `json:"kind"` // "iso" or "img"
	ReadOnly bool        `json:"read_only"`
	MappedTo MediaTarget `json:"mapped_to,omitempty"`
	// Targets lists the drives this image can be mapped to.
	Targets []MediaTarget `json:"targets"`
}

// MediaDrive is a virtual drive with an image attached (or being attached).
type MediaDrive struct {
	Target       MediaTarget `json:"target"`
	ImageID      string      `json:"image_id"`
	Image        string      `json:"image"`
	Mounting     bool        `json:"mounting"`
	ReadOnly     bool        `json:"read_only"`
	BytesRead    int64       `json:"bytes_read"`
	BytesWritten int64       `json:"bytes_written"`
}

// MediaSample is one second of transfer, summed over all drives.
type MediaSample struct {
	At      int64 `json:"at"` // Unix milliseconds
	Read    int64 `json:"read"`
	Written int64 `json:"written"`
}

// MediaStatus is the whole virtual media picture for the UI.
type MediaStatus struct {
	Images  []MediaImage  `json:"images"`
	Drives  []MediaDrive  `json:"drives"`
	History []MediaSample `json:"history"`         // up to the last few minutes, oldest first
	Error   string        `json:"error,omitempty"` // the latest failure, if any
}

// VirtualMedia is implemented by consoles that can present local images to the host.
type VirtualMedia interface {
	AddImage(path string, readOnly bool) (MediaImage, error)
	RemoveImage(id string) error
	SetImageReadOnly(id string, readOnly bool) error
	// MapImage attaches an image; it returns once the BMC has accepted it.
	MapImage(id string, target MediaTarget) error
	// UnmapDrive ejects a drive, or cancels an attach still in progress.
	UnmapDrive(target MediaTarget) error
	// USBReset reconnects the BMC's virtual mass-storage device on the host.
	USBReset() error
	MediaStatus() MediaStatus
}
