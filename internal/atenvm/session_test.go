package atenvm_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"ipmi-connect/internal/atenvm"
	"ipmi-connect/internal/vmedia"
)

type fakeImage struct {
	data []byte
}

func (f *fakeImage) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	return n, nil
}

func (f *fakeImage) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > int64(len(f.data)) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(f.data[off:], p)
	return n, nil
}

func TestSessionLifecycleAndCBWLoop(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess := atenvm.NewSession(clientConn, atenvm.SessionOptions{
		Username:          "tokenABC",
		Password:          "tokenABC",
		IsSessionID:       true,
		Encrypt:           true,
		KeepaliveInterval: 10 * time.Millisecond,
	})

	serverDone := make(chan struct{})

	// Mock BMC Server
	go func() {
		defer close(serverDone)

		// 1. Read Setup Request
		hSetup, _, err := atenvm.ReadFrame(serverConn)
		if err != nil {
			t.Errorf("server: read setup failed: %v", err)
			return
		}
		if hSetup.Tag != atenvm.TagDeviceSetupReq {
			t.Errorf("server: tag = 0x%02x, want 0x01", hSetup.Tag)
			return
		}

		// 2. Reply Setup Response
		hResp := atenvm.Header{
			Endpoint:   atenvm.EndpointControl,
			Flags:      0x00,
			DevPort:    0x00,
			Tag:        atenvm.TagDeviceSetupResp,
			PayloadLen: 9,
		}
		respPayload := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x00, 0x00, 0x00, 0x00}
		if err := atenvm.WriteFrame(serverConn, hResp, respPayload); err != nil {
			t.Errorf("server: write setup resp failed: %v", err)
			return
		}

		// 3. Read EP Setup
		hEP, _, err := atenvm.ReadFrame(serverConn)
		if err != nil {
			t.Errorf("server: read EP setup failed: %v", err)
			return
		}
		if hEP.Tag != atenvm.TagEPSetup {
			t.Errorf("server: tag = 0x%02x, want 0x07", hEP.Tag)
			return
		}

		// 4. Send INQUIRY CBW from Host (Bulk OUT, endpoint 0x11, tag 0x00)
		cbwInquiry := make([]byte, atenvm.CBWLength)
		binary.LittleEndian.PutUint32(cbwInquiry[0:4], atenvm.CBWSignature) // "USBC"
		binary.LittleEndian.PutUint32(cbwInquiry[4:8], 0x100)               // dCBWTag
		binary.LittleEndian.PutUint32(cbwInquiry[8:12], 36)                 // dCBWDataTransferLength
		cbwInquiry[12] = 0x80                                               // Device to host (IN)
		cbwInquiry[14] = 6                                                  // CDB len
		cbwInquiry[15] = atenvm.CmdInquiry
		cbwInquiry[19] = 36 // alloc len

		hCBW := atenvm.Header{
			Endpoint:   atenvm.EndpointBulkOut,
			Flags:      atenvm.FlagBMCQueue,
			DevPort:    1,
			Tag:        atenvm.TagEnqueue,
			PayloadLen: atenvm.CBWLength,
		}
		if err := atenvm.WriteFrame(serverConn, hCBW, cbwInquiry); err != nil {
			t.Errorf("server: write inquiry CBW failed: %v", err)
			return
		}

		// 5. Read INQUIRY Data PDU (Bulk IN, endpoint 0x22, tag 0x00)
		hData, inqData, err := atenvm.ReadFrame(serverConn)
		if err != nil {
			t.Errorf("server: read inquiry data failed: %v", err)
			return
		}
		if hData.Endpoint != atenvm.EndpointBulkIn || hData.Tag != atenvm.TagEnqueue || len(inqData) != 36 {
			t.Errorf("server: unexpected inquiry data header/payload: %+v (len %d)", hData, len(inqData))
			return
		}
		if !bytes.Contains(inqData, []byte("Virtual CDROM")) {
			t.Errorf("server: inquiry data does not contain product string")
			return
		}

		// 6. Read INQUIRY CSW (Bulk IN, endpoint 0x22, tag 0xFF)
		hCSW, cswBytes, err := atenvm.ReadFrame(serverConn)
		if err != nil {
			t.Errorf("server: read inquiry CSW failed: %v", err)
			return
		}
		if hCSW.Tag != atenvm.TagEnqueueAndGo || len(cswBytes) != atenvm.CSWLength {
			t.Errorf("server: unexpected inquiry CSW header: %+v", hCSW)
			return
		}
		csw, err := atenvm.ParseCSW(cswBytes)
		if err != nil || csw.Tag != 0x100 || csw.Status != atenvm.CSWStatusGood {
			t.Errorf("server: unexpected inquiry CSW: %+v, err=%v", csw, err)
			return
		}

		// 7. Send READ(10) CBW for LBA 0, 1 sector (2048 B)
		cbwRead := make([]byte, atenvm.CBWLength)
		binary.LittleEndian.PutUint32(cbwRead[0:4], atenvm.CBWSignature)
		binary.LittleEndian.PutUint32(cbwRead[4:8], 0x101)
		binary.LittleEndian.PutUint32(cbwRead[8:12], 2048)
		cbwRead[12] = 0x80
		cbwRead[14] = 10
		cbwRead[15] = atenvm.CmdRead10
		binary.BigEndian.PutUint32(cbwRead[17:21], 0) // LBA 0
		binary.BigEndian.PutUint16(cbwRead[22:24], 1) // 1 sector

		if err := atenvm.WriteFrame(serverConn, hCBW, cbwRead); err != nil {
			t.Errorf("server: write read CBW failed: %v", err)
			return
		}

		// 8. Read READ(10) Data PDU
		hDataRead, readData, err := atenvm.ReadFrame(serverConn)
		if err != nil || len(readData) != 2048 {
			t.Errorf("server: read sector data failed: %+v (len %d), err=%v", hDataRead, len(readData), err)
			return
		}

		// 9. Read READ(10) CSW
		hCSWRead, cswReadBytes, err := atenvm.ReadFrame(serverConn)
		if err != nil {
			t.Errorf("server: read read CSW failed: %v", err)
			return
		}
		cswRead, err := atenvm.ParseCSW(cswReadBytes)
		if err != nil || cswRead.Tag != 0x101 || cswRead.Status != atenvm.CSWStatusGood || hCSWRead.Tag != atenvm.TagEnqueueAndGo {
			t.Errorf("server: unexpected read CSW: %+v, err=%v", cswRead, err)
			return
		}

		// 10. Send Keepalive Ping Request (Tag 0x04)
		hPing := atenvm.Header{
			Endpoint:   atenvm.EndpointControl,
			Flags:      0x00,
			DevPort:    0x00,
			Tag:        atenvm.TagPingReq,
			PayloadLen: 4,
		}
		if err := atenvm.WriteFrame(serverConn, hPing, []byte{0xaa, 0xbb, 0xcc, 0xdd}); err != nil {
			t.Errorf("server: write ping failed: %v", err)
			return
		}

		// 11. Read Ping Response (Tag 0x03)
		hPong, pongData, err := atenvm.ReadFrame(serverConn)
		if err != nil {
			t.Errorf("server: read pong failed: %v", err)
			return
		}
		if hPong.Tag != atenvm.TagPingResp || !bytes.Equal(pongData, []byte{0xaa, 0xbb, 0xcc, 0xdd}) {
			t.Errorf("server: unexpected pong payload: %+v %v", hPong, pongData)
			return
		}

		// 12. Read Detach Request (Tag 0x05)
		hDetach, _, err := atenvm.ReadFrame(serverConn)
		if err != nil {
			return
		}
		if hDetach.Tag == atenvm.TagDetachReq {
			hDetachResp := atenvm.Header{
				Endpoint:   atenvm.EndpointControl,
				Flags:      0x00,
				DevPort:    0x00,
				Tag:        atenvm.TagDetachResp,
				PayloadLen: 1,
			}
			_ = atenvm.WriteFrame(serverConn, hDetachResp, []byte{0x00})
		}
	}()

	// 1. Open session
	if err := sess.Open(ctx); err != nil {
		t.Fatalf("sess.Open failed: %v", err)
	}

	// 2. Map ISO image
	fakeData := make([]byte, 8192)
	copy(fakeData, "CD-ROM ISO DATA HEADER")
	img := &fakeImage{data: fakeData}
	disk, err := sess.Map(ctx, img, vmedia.MapOptions{
		Target:   vmedia.TargetCD,
		Size:     int64(len(fakeData)),
		ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("sess.Map failed: %v", err)
	}
	if disk != uint16(vmedia.TargetCD) {
		t.Errorf("disk = %d, want %d", disk, vmedia.TargetCD)
	}

	// Wait for server exchange to finish
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server exchange")
	}

	// Verify stats
	drives := sess.Drives()
	if len(drives) != 1 {
		t.Fatalf("drives count = %d, want 1", len(drives))
	}
	if drives[0].BytesRead != 36+2048 {
		t.Errorf("BytesRead = %d, want %d", drives[0].BytesRead, 36+2048)
	}

	// 3. Unmap and Close
	if err := sess.Unmap(disk); err != nil {
		t.Errorf("sess.Unmap failed: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Errorf("sess.Close failed: %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(1 * time.Second):
		t.Error("sess.Done was not closed after Close()")
	}
}
