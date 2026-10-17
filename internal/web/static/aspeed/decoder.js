/*
 * Copyright (C) 2021 Aspeed Tech. Authors
 * Licensed under MPL 2.0 (see LICENSE.txt)
 *
 * ipmi-connect: reduced to the JS decoder only. The original also loaded a WASM build and
 * switched to it once ready, but each decoder keeps its own previous frame for delta updates,
 * so switching mid-stream painted deltas over an empty frame. The JS decoder also accepts a
 * separate chroma table selector, which Avocent streams need.
 */
import AspeedJpeg from "./decoder_js.js";

const dec = new AspeedJpeg();

export function decode(header, inbuf) {
    const pixelCount = header.width * header.height;
    dec.decode(header, inbuf);
    const out = new Uint8ClampedArray(dec.outbuf.buffer, dec.outbuf.byteOffset, pixelCount * 4);
    // The bitstream carries only YUV; force full opacity so the canvas does not blend.
    const view32 = new Uint32Array(out.buffer, out.byteOffset, pixelCount);
    const alphaMask = 0xFF000000 >>> 0;
    for (let i = 0; i < view32.length; i++) {
        view32[i] |= alphaMask;
    }
    return out;
}
