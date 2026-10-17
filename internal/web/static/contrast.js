// True-black calibration for the AST2300's analog VGA capture.
//
// The ADC digitises black at ~101 and white at ~135, so BIOS screens look washed-out
// grey. When a frame shows that DC pedestal, a lookup table stretches it back to 0-255.
// Full-range images (normal OS desktops, Supermicro) keep an identity table.

const PEDESTAL_MIN_P01 = 30;       // darkest 0.5% must sit this high to count as a pedestal
const PEDESTAL_MAX_SPAN = 160;     // and the usable range must be this narrow
const BACKGROUND_MIN_SHARE = 0.15; // a mode covering this much of the frame is the background
const BACKGROUND_NEAR_P01 = 10;    // and is treated as black if it sits this close to p01

export function identityLUT() {
  const lut = new Uint8Array(256);
  for (let i = 0; i < 256; i++) lut[i] = i;
  return lut;
}

// buildContrastLUT derives a LUT from the red channel of an RGBA frame.
export function buildContrastLUT(pixels) {
  const hist = new Int32Array(256);
  const total = pixels.length >>> 2;
  for (let i = 0; i < pixels.length; i += 4) {
    hist[pixels[i]]++;
  }

  // Dominant peak (background in console/POST screens)
  let modeVal = 0, modeCount = 0;
  for (let i = 0; i < 256; i++) {
    if (hist[i] > modeCount) {
      modeCount = hist[i];
      modeVal = i;
    }
  }

  // Percentiles
  let cum = 0;
  let p01 = -1, p99 = 255;
  for (let i = 0; i < 256; i++) {
    cum += hist[i];
    if (p01 === -1 && cum >= total * 0.005) {
      p01 = i;
    }
    if (cum >= total * 0.999) {
      p99 = i;
      break;
    }
  }

  const hasPedestal = p01 >= PEDESTAL_MIN_P01 && (p99 - p01) <= PEDESTAL_MAX_SPAN && p99 > p01;
  if (!hasPedestal) {
    return identityLUT();
  }

  let low = p01;
  if (modeCount >= total * BACKGROUND_MIN_SHARE && Math.abs(modeVal - p01) <= BACKGROUND_NEAR_P01 && modeVal < p99) {
    low = modeVal;
  }
  const high = p99;
  const scale = 255 / (high - low);
  const lut = new Uint8Array(256);
  for (let i = 0; i < 256; i++) {
    if (i <= low) {
      lut[i] = 0;
    } else if (i >= high) {
      lut[i] = 255;
    } else {
      lut[i] = Math.min(255, Math.max(0, Math.round((i - low) * scale)));
    }
  }
  return lut;
}

// applyLUT rewrites the RGB channels in place, leaving alpha alone.
export function applyLUT(lut, pixels) {
  if (lut[0] === 0 && lut[255] === 255 && lut[128] === 128) {
    return; // Identity LUT
  }
  for (let i = 0; i < pixels.length; i += 4) {
    pixels[i] = lut[pixels[i]];
    pixels[i + 1] = lut[pixels[i + 1]];
    pixels[i + 2] = lut[pixels[i + 2]];
  }
}
