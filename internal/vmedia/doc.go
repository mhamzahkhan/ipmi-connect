// Package vmedia manages virtual media for a console: a library of local disc and disk images,
// which of them are attached to which virtual drive, and transfer statistics. It drives an AVMP
// session that it opens when the first drive is attached and closes after the last, so the BMC
// is free for other clients in between.
package vmedia
