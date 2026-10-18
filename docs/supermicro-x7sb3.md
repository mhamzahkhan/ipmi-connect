# Supermicro X7SB3 Deep Dive

## System Profile

- **Device**: Supermicro Server (Motherboard: X7SB3)
- **BMC Hardware**: Winbond / Nuvoton WPCM450 (ARM926EJ-S core)
- **Firmware Version**: `0313` / `3.13` (Built Jan 02 2014)
- **Firmware Tag**: `BL_SUPERMICRO_X7SB3_2014-01-02_B`
- **Host Architecture**: Intel 3200 Chipset / LGA775
- **Firmware Vendor**: ATEN International Co., Ltd.

---

## 1. Web Architecture & Authentication Peculiarities

### 1.1 Web Server & Endpoints
- **Web Server**: `lighttpd/1.4.23`
- **Ports**: Plain HTTP on port `80`, HTTPS on port `443` (TLSv1 only, `AES256-SHA`).
- **Certificate**: Self-signed certificate issued by Supermicro.

### 1.2 Authentication Endpoint
- **URL**: `POST https://<bmc>/cgi/login.cgi`
- **Content-Type**: `application/x-www-form-urlencoded`
- **Payload**: `name=root&pwd=<password>`
- **Response**: Cookie `SID=<16_hex_chars>` (e.g. `SID=4cb8a2360b37ab75`)

### 1.3 CRITICAL: The 500 Internal Server Error Cookie Bug
> [!IMPORTANT]
> The lighttpd CGI script `/cgi/url_redirect.cgi?url_name=ikvm&url_type=jwsk` crashes with `HTTP 500 Internal Server Error` if invoked as a naive HTTP client.
>
> **Root Cause**: The CGI script checks for strict session state cookies and referer headers!
> **Mandatory Headers**:
> ```http
> Cookie: SID=<sid_hex>; langSetFlag=0; language=English; mainpage=remote; subpage=man_ikvm
> Referer: https://<bmc>/cgi/url_redirect.cgi?url_name=man_ikvm
> User-Agent: Mozilla/5.0
> ```

---

## 2. ATEN iKVM Wire Protocol (Port 5900)

Remote console video and mouse/keyboard redirection run on **TCP Port 5900** using an extended RFB (Remote Frame Buffer / VNC) protocol.

### 2.1 Initial Version Exchange
1. Server sends 12 ASCII bytes:
   ```text
   RFB 003.008\n
   ```
2. Client echoes back the exact 12 ASCII bytes.

### 2.2 Security Negotiation
1. Server sends 1 byte: number of supported security types (e.g., `1`).
2. Server sends that number of bytes: each byte is a supported security code (e.g., `0x01` = None, `0x02` = VNC auth, `0x10` = ATEN auth).
3. Client replies with 1 byte selecting the chosen security type.

### 2.3 ATEN Extended Authentication
1. Server sends a 24-byte random challenge nonce.
2. Client sends 24 bytes of username (padded with `0x00`).
3. Client sends 24 bytes of authentication response / session token (padded with `0x00`).
   `ipmi-connect` currently sends the plain BMC password here; whether the firmware expects the web `SID` instead is unverified.
4. Server returns a 4-byte big-endian integer:
   - `0x00000000`: Authentication Success!
   - Non-zero: Authentication failed, followed by error message length and string.

### 2.4 Client & Server Initialization
1. Client sends 1 byte `shared-flag` (`0x00` = exclusive console, `0x01` = shared console).
2. Server responds with `ServerInit` structure:
   - `width`: uint16 big-endian (e.g. 1024)
   - `height`: uint16 big-endian (e.g. 768)
   - `pixel_format`: 16 bytes (bits-per-pixel, depth, big-endian-flag, true-color-flag, red-max, green-max, blue-max, red-shift, green-shift, blue-shift, 3 pad bytes)
   - `name_length`: uint32 big-endian
   - `name_string`: Server name string (ASCII)

### 2.5 Video Frame Streaming & Tile Compression
ATEN firmware transmits tile-based updates:
- Message type `0` = FramebufferUpdate.
- Rectangle headers describe changed tile regions `(x, y, w, h)` and encoding type.
- ATEN custom encoding uses RLE (Run Length Encoding) and DCT/JPEG-like block compression directly implemented on the Winbond WPCM450 hardware video engine.

---

## 3. Virtual Media & Power Control Architecture

### 3.1 Virtual Storage (USB Mass Storage Emulation)
Supermicro ATEN BMC supports virtual media through two mechanisms:
1. **USB Redirection over RMCP/TCP Port 623**:
   The vendor viewer's native virtual storage library carries USB mass-storage traffic to the BMC over TCP port 623.
2. **CIFS/HTTP ISO Share Mount**:
   The BMC web server exposes `/cgi/virtual_media_share_img.cgi` where the BMC mounts an ISO or floppy image from a network share or HTTP server directly into the host BIOS virtual CD/DVD-ROM.

### 3.2 Power Control Endpoints
- **URL**: `POST https://<bmc>/cgi/power_ctrl.cgi`
- **Body**: `web_power_control=<code>` (form-encoded), with the session cookies and referer from §1.3
- **Codes** (as sent by `ipmi-connect`; not yet confirmed against the live BMC):
  - `1`: Power on
  - `2`: Immediate hard power off
  - `3`: Hard reset
  - `4`: Power cycle (off then on)
  - `5`: Graceful ACPI shutdown
