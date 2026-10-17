// Package avmp implements the client side of Avocent's virtual media protocol (AVMP) as spoken by
// the Dell C6220's MergePoint BMC: it exposes a local disc image to the host as a USB CD-ROM.
//
// The BMC emulates the USB mass-storage device and SCSI itself and asks the client only for
// blocks, so serving an ISO is a matter of answering block reads. The layouts follow the vendor's
// avctVM.jar (see the research notes, dell-avmp-virtual-media.md). Reaching an AVMP-ready socket
// (APCP greeting with mode 2, TLS) and minting credentials is the caller's job.
package avmp
