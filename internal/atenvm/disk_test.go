package atenvm_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"ipmi-connect/internal/atenvm"
	"ipmi-connect/internal/vmedia"
)

func TestDiskTargetSCSI(t *testing.T) {
	fakeData := make([]byte, 512*10)
	copy(fakeData, "SECTOR 0 DATA")
	img := &fakeImage{data: fakeData}
	target := atenvm.NewDiskTarget(img, int64(len(fakeData)), false)

	// 1. INQUIRY (36 bytes)
	cdbInq := []byte{atenvm.CmdInquiry, 0, 0, 0, 36, 0}
	data, status, _ := target.ExecuteCDB(cdbInq, 36)
	if status != atenvm.CSWStatusGood || len(data) != 36 {
		t.Fatalf("INQUIRY failed: status=0x%02x, len=%d", status, len(data))
	}
	if data[0] != 0x00 { // Direct Access Block Device
		t.Errorf("peripheral device type = 0x%02x, want 0x00", data[0])
	}
	if data[1] != 0x80 { // Removable
		t.Errorf("RMB = 0x%02x, want 0x80", data[1])
	}
	if !bytes.Contains(data, []byte("Virtual Disk")) {
		t.Errorf("product string missing from disk inquiry")
	}

	// 2. READ CAPACITY (8 bytes)
	cdbCap := []byte{atenvm.CmdReadCapacity10, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	capData, status, _ := target.ExecuteCDB(cdbCap, 8)
	if status != atenvm.CSWStatusGood || len(capData) != 8 {
		t.Fatalf("READ CAPACITY failed: status=0x%02x, len=%d", status, len(capData))
	}
	lastLBA := binary.BigEndian.Uint32(capData[0:4])
	blkSize := binary.BigEndian.Uint32(capData[4:8])
	if lastLBA != 9 || blkSize != 512 {
		t.Errorf("lastLBA=%d (want 9), blkSize=%d (want 512)", lastLBA, blkSize)
	}

	// 3. MODE SENSE (6) Write Protect flag
	cdbMode := []byte{atenvm.CmdModeSense6, 0, 0, 0, 4, 0}
	modeData, _, _ := target.ExecuteCDB(cdbMode, 4)
	if modeData[2]&0x80 != 0 {
		t.Errorf("write protect bit set on writable disk")
	}

	roTarget := atenvm.NewDiskTarget(img, int64(len(fakeData)), true)
	roModeData, _, _ := roTarget.ExecuteCDB(cdbMode, 4)
	if roModeData[2]&0x80 == 0 {
		t.Errorf("write protect bit not set on read-only disk")
	}

	// 4. READ(10)
	cdbRead := []byte{atenvm.CmdRead10, 0, 0, 0, 0, 0, 0, 0, 1, 0}
	readData, status, _ := target.ExecuteCDB(cdbRead, 512)
	if status != atenvm.CSWStatusGood || len(readData) != 512 {
		t.Fatalf("READ(10) failed: status=0x%02x, len=%d", status, len(readData))
	}
	if !bytes.HasPrefix(readData, []byte("SECTOR 0 DATA")) {
		t.Errorf("read data mismatch")
	}

	// 5. WRITE(10) data execution
	cdbWrite := []byte{atenvm.CmdWrite10, 0, 0, 0, 0, 2, 0, 0, 1, 0} // LBA 2
	writeData := []byte("NEW SECTOR 2 DATA OVERWRITE")
	payload := make([]byte, 512)
	copy(payload, writeData)
	writeStatus := target.WriteData(cdbWrite, payload)
	if writeStatus != atenvm.CSWStatusGood {
		t.Fatalf("WriteData failed: status=0x%02x", writeStatus)
	}

	// Verify write took effect on image at offset 2 * 512 = 1024
	if !bytes.HasPrefix(img.data[1024:], writeData) {
		t.Errorf("written data not present at offset 1024")
	}

	// Verify write on read-only target fails
	roStatus := roTarget.WriteData(cdbWrite, payload)
	if roStatus != atenvm.CSWStatusCommandFail {
		t.Errorf("write succeeded on read-only target")
	}
}

func TestSessionBulkOutWrite(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess := atenvm.NewSession(clientConn, atenvm.SessionOptions{
		Username:    "token123",
		Password:    "token123",
		IsSessionID: true,
		Encrypt:     false,
	})

	serverDone := make(chan struct{})

	go func() {
		defer close(serverDone)

		// 1. Setup handshake
		_, _, _ = atenvm.ReadFrame(serverConn)
		hResp := atenvm.Header{
			Endpoint:   atenvm.EndpointControl,
			Flags:      0x00,
			DevPort:    0x00,
			Tag:        atenvm.TagDeviceSetupResp,
			PayloadLen: 9,
		}
		_ = atenvm.WriteFrame(serverConn, hResp, []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x00, 0x00, 0x00, 0x00})
		_, _, _ = atenvm.ReadFrame(serverConn) // EP Setup

		// 2. Send WRITE(10) CBW (Flags=0x00 host-to-device, DataLen=512)
		cbwWrite := make([]byte, atenvm.CBWLength)
		binary.LittleEndian.PutUint32(cbwWrite[0:4], atenvm.CBWSignature)
		binary.LittleEndian.PutUint32(cbwWrite[4:8], 0x777) // tag
		binary.LittleEndian.PutUint32(cbwWrite[8:12], 512)  // len
		cbwWrite[12] = 0x00                                 // Host to Device
		cbwWrite[14] = 10
		cbwWrite[15] = atenvm.CmdWrite10
		binary.BigEndian.PutUint32(cbwWrite[17:21], 1) // LBA 1
		binary.BigEndian.PutUint16(cbwWrite[22:24], 1) // 1 block

		hCBW := atenvm.Header{
			Endpoint:   atenvm.EndpointBulkOut,
			Flags:      atenvm.FlagBMCQueue,
			DevPort:    1,
			Tag:        atenvm.TagEnqueue,
			PayloadLen: atenvm.CBWLength,
		}
		_ = atenvm.WriteFrame(serverConn, hCBW, cbwWrite)

		// 3. Send Bulk-OUT Data Frame (512 bytes)
		writePayload := make([]byte, 512)
		copy(writePayload, "WRITTEN VIA BULK-OUT")
		hData := atenvm.Header{
			Endpoint:   atenvm.EndpointBulkOut,
			Flags:      0x00,
			DevPort:    1,
			Tag:        atenvm.TagEnqueue,
			PayloadLen: 512,
		}
		_ = atenvm.WriteFrame(serverConn, hData, writePayload)

		// 4. Read CSW from client
		_, cswBytes, err := atenvm.ReadFrame(serverConn)
		if err != nil {
			t.Errorf("read CSW failed: %v", err)
			return
		}
		csw, err := atenvm.ParseCSW(cswBytes)
		if err != nil || csw.Tag != 0x777 || csw.Status != atenvm.CSWStatusGood {
			t.Errorf("unexpected write CSW: %+v, err=%v", csw, err)
			return
		}
	}()

	if err := sess.Open(ctx); err != nil {
		t.Fatalf("sess.Open: %v", err)
	}

	diskData := make([]byte, 2048)
	img := &fakeImage{data: diskData}
	disk, err := sess.Map(ctx, img, vmedia.MapOptions{
		Target:   vmedia.TargetFloppy,
		Size:     int64(len(diskData)),
		ReadOnly: false,
	})
	if err != nil {
		t.Fatalf("sess.Map: %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server exchange")
	}

	// Verify data written at offset 1 * 512 = 512
	if !bytes.HasPrefix(diskData[512:], []byte("WRITTEN VIA BULK-OUT")) {
		t.Errorf("diskData[512:] does not contain written data: %q", diskData[512:540])
	}

	// Verify stats
	drives := sess.Drives()
	if len(drives) != 1 || drives[0].BytesWritten != 512 {
		t.Errorf("drives stats mismatch: %+v", drives)
	}

	_ = sess.Unmap(disk)
	_ = sess.Close()
}
