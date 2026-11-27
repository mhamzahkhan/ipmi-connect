## [0.2.1](https://github.com/mhamzahkhan/ipmi-connect/compare/v0.2.0...v0.2.1) (2026-11-27)

### Bug Fixes

* **atenvm:** bound reads that Execute returns as one buffer ([466cd80](https://github.com/mhamzahkhan/ipmi-connect/commit/466cd80c96b0b8d352cbfbddc2328ce000952821))
* **atenvm:** handle truncated READ TOC and MODE SENSE(10) CDBs ([6dd3a9a](https://github.com/mhamzahkhan/ipmi-connect/commit/6dd3a9ad6021941e9d451afa910205431cd14d70))
* **atenvm:** read large frame payloads as their bytes arrive ([45e0a0c](https://github.com/mhamzahkhan/ipmi-connect/commit/45e0a0c4248869de84c738d2d4d86425ae4bdad1))
* **dell:** drop video frames of implausible geometry ([6d362d1](https://github.com/mhamzahkhan/ipmi-connect/commit/6d362d10d0d7553c53a0aacfaaccb80eeb6c5bce))
* quote the remaining request strings in log lines ([b2031b6](https://github.com/mhamzahkhan/ipmi-connect/commit/b2031b618e71780f75527f5055abb1255acc6004))
* **supermicro:** allocate at most one picture per framebuffer update ([e3c48b9](https://github.com/mhamzahkhan/ipmi-connect/commit/e3c48b9b4f8406fdd252e61cbaa24cff8d93ed38))
* **supermicro:** cap total framebuffer update size ([37439d4](https://github.com/mhamzahkhan/ipmi-connect/commit/37439d4e207c8a480996480a7b18d46e4eb2ef34))
* **supermicro:** reject implausibly long JNLP KVM tokens ([8f6df52](https://github.com/mhamzahkhan/ipmi-connect/commit/8f6df52e3bb0365ac0d1a40716a7459ed1885e25))
* **supermicro:** ride out host-reboot outages and reclaim keyboard/mouse control ([e2aae33](https://github.com/mhamzahkhan/ipmi-connect/commit/e2aae3347099b4a8e0d06867718c387827e362a3))
* **vmedia:** refuse an image file replaced since it was added ([0475a45](https://github.com/mhamzahkhan/ipmi-connect/commit/0475a45fb30d4d0e682f9ca5335a753838ff02bc))
* **web:** quote the macro name in its log line ([b3890e2](https://github.com/mhamzahkhan/ipmi-connect/commit/b3890e2e4bf29aa264bbaa5c66731a424af512d0)), closes [#11](https://github.com/mhamzahkhan/ipmi-connect/issues/11)

## [0.2.0](https://github.com/mhamzahkhan/ipmi-connect/compare/v0.1.0...v0.2.0) (2026-10-18)

### Features

* **cli:** add -v/--version flag ([6fe429a](https://github.com/mhamzahkhan/ipmi-connect/commit/6fe429a3d660911aa4c631cd6ae7a6222f37a2a1))

### Bug Fixes

* **ci:** gate release on CI, harden renovate automerge, and add buildinfo fallback ([08d7034](https://github.com/mhamzahkhan/ipmi-connect/commit/08d7034862a59088de4355c575d2efe479e62df3))
* **ci:** use golangci-lint v2 and fix flaky atenvm detach test ([fdbe112](https://github.com/mhamzahkhan/ipmi-connect/commit/fdbe1120b13a36ca14797858a27bf64c12cc3169))
