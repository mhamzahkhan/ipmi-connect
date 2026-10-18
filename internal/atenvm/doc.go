// Package atenvm implements the client side of the ATEN iKVM Virtual Storage protocol
// (USBAnywhere over TCP port 623) spoken by Supermicro BMCs (such as the X7SB3 Nuvoton WPCM450).
//
// Unlike Dell AVMP where the BMC performs SCSI commands and only requests raw block reads
// from the client, in ATEN Virtual Storage the roles are inverted:
//   - The BMC connects to the host machine as a USB device and bridges USB packets over TCP.
//   - The client emulates the USB mass-storage peripheral (USB descriptors, Bulk-Only Transport,
//     and SCSI/MMC target commands like INQUIRY, READ CAPACITY, READ TOC, READ(10)).
//
// Communication occurs over a single TCP connection to port 623. The Device Setup packet
// carries credentials and USB descriptor tables (optionally RC4-encrypted with a fixed key).
// Once plugged in, the BMC streams Command Block Wrappers (CBWs), and the client responds
// with SCSI data and Command Status Wrappers (CSWs).
package atenvm
