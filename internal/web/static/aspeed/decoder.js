/*
 * Copyright (C) 2021 Aspeed Tech. Authors
 * Licensed under MPL 2.0 (see LICENSE.txt)
 *
 */
import AspeedJpeg from "./decoder_js.js";
import Module from './decoder_wasm.js';

let dec = null;
const wasm = { mod: null, srcPtr: 0, srcSize: 0, outPtr: 0, outSize: 0 };

Module().then(m => {
    m._init();
    wasm.mod = m;
}).catch(err => {
    console.warn("WASM decoder initialization failed, fallback to JS decoder:", err);
});

if (dec === null) {
    dec = new AspeedJpeg();
}

function _ensureWasmBuffers(srcLen, outLen) {
    if (!wasm.srcPtr || wasm.srcSize < srcLen) {
        if (wasm.srcPtr) wasm.mod._free(wasm.srcPtr);
        wasm.srcSize = Math.max(srcLen + 4096, 65536);
        wasm.srcPtr = wasm.mod._malloc(wasm.srcSize);
    }
    if (!wasm.outPtr || wasm.outSize < outLen) {
        if (wasm.outPtr) wasm.mod._free(wasm.outPtr);
        wasm.outSize = outLen;
        wasm.outPtr = wasm.mod._malloc(wasm.outSize);
    }
}

export function decode(header, inbuf) {
    const pixelCount = header.width * header.height;
    const outBytes = pixelCount * 4;

    if (wasm.mod != null) {
        try {
            _ensureWasmBuffers(inbuf.length, outBytes);
            wasm.mod.HEAPU8.set(inbuf, wasm.srcPtr);
            wasm.mod._decode(wasm.srcPtr, inbuf.length, wasm.outPtr,
                             header.width, header.height, header.mode420,
                             header.selector, header.advance_selector);

            // Reconstruct view directly from HEAPU8.buffer to guard against heap reallocation detachment
            const out = new Uint8ClampedArray(wasm.mod.HEAPU8.buffer, wasm.outPtr, outBytes);
            // Guarantee full opacity (Alpha = 255) for every pixel
            const view32 = new Uint32Array(out.buffer, out.byteOffset, pixelCount);
            const alphaMask = 0xFF000000 >>> 0;
            for (let i = 0; i < view32.length; i++) {
                view32[i] |= alphaMask;
            }
            return out;
        } catch (e) {
            console.warn("WASM decode encountered an error, using JS decoder:", e);
        }
    }

    if (dec === null) {
        dec = new AspeedJpeg();
    }
    dec.decode(header, inbuf);
    const out = new Uint8ClampedArray(dec.outbuf.buffer, dec.outbuf.byteOffset, outBytes);
    // Guarantee full opacity (Alpha = 255) for every pixel
    const view32 = new Uint32Array(out.buffer, out.byteOffset, pixelCount);
    const alphaMask = 0xFF000000 >>> 0;
    for (let i = 0; i < view32.length; i++) {
        view32[i] |= alphaMask;
    }
    return out;
}
