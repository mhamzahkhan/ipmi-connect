# Documentation

Reverse-engineering notes for the BMCs that `ipmi-connect` talks to. Examples use `<bmc>` for the BMC's address.

| Document | Contents |
| :--- | :--- |
| [protocols.md](protocols.md) | Wire formats: Avocent APCP/BEEF packets, Dell AppWeb API, ATEN RFB |
| [dell-c6220.md](dell-c6220.md) | Dell C6220 II deep dive: auth flow, JNLP tokens, video engine, keepalives |
| [supermicro-x7sb3.md](supermicro-x7sb3.md) | Supermicro X7SB3 deep dive: CGI quirks, Pack200 unpacking, RFB auth, power |

## Tested hardware & firmware

| | Dell PowerEdge C6220 II | Supermicro X7SB3 |
| :--- | :--- | :--- |
| BMC SoC | ASPEED AST2300 (ASIC type 7) | Winbond/Nuvoton WPCM450 (ARM926EJ-S) |
| Firmware | `2.62.5` (May 21 2015) | `0313` / `3.13` (Jan 02 2014), tag `BL_SUPERMICRO_X7SB3_2014-01-02_B` |
| Firmware vendor | Avocent MergePoint Embedded | ATEN |
| Host | C6220 II, BIOS 2.10.0 | X7SB3, Intel 3200 / LGA775 |
| Web server | Mbedthis-Appweb 2.4.2 (HTTPS 443) | lighttpd 1.4.23 (HTTP 80, HTTPS 443) |
| TLS | TLSv1 only, `DHE-RSA-AES256-SHA` | TLSv1 only, `AES256-SHA` |
| KVM | TCP 7578 (APCP) | TCP 5900 (RFB) |
| Virtual media | TCP 7578 (AVMP) | TCP 623 (RMCP/USB) or CIFS/HTTP share |
| Default user | `root` | `root` |
