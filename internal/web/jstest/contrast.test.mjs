// Run with: make test-js
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { buildContrastLUT, applyLUT, identityLUT } from '../static/contrast.js';

// rgba builds an RGBA buffer of grey pixels from [value, count] pairs.
function rgba(...runs) {
  const total = runs.reduce((n, [, count]) => n + count, 0);
  const px = new Uint8ClampedArray(total * 4);
  let i = 0;
  for (const [value, count] of runs) {
    for (let c = 0; c < count; c++, i += 4) {
      px[i] = px[i + 1] = px[i + 2] = value;
      px[i + 3] = 255;
    }
  }
  return px;
}

test('full-range image leaves the LUT as identity', () => {
  const lut = buildContrastLUT(rgba([0, 500], [128, 300], [255, 200]));
  assert.deepEqual(lut, identityLUT());
});

test('AST2300 pedestal (black ~101, white ~135) is stretched to true black and white', () => {
  const lut = buildContrastLUT(rgba([101, 900], [135, 100]));
  assert.equal(lut[101], 0, 'pedestal black becomes #000');
  assert.equal(lut[90], 0, 'below black clamps to 0');
  assert.equal(lut[135], 255, 'pedestal white becomes #fff');
  assert.equal(lut[118], Math.round((118 - 101) * (255 / 34)), 'midtones scale linearly');
});

test('dominant background near p01 is used as the black point', () => {
  // p01 lands on 100 (a few dark pixels); the 80% background at 104 should become black.
  const lut = buildContrastLUT(rgba([100, 10], [104, 800], [135, 190]));
  assert.equal(lut[104], 0);
});

test('applyLUT rewrites RGB but never alpha, and skips identity', () => {
  const lut = buildContrastLUT(rgba([101, 900], [135, 100]));
  const px = rgba([135, 1]);
  px[3] = 7;
  applyLUT(lut, px);
  assert.deepEqual([...px], [255, 255, 255, 7]);

  const untouched = rgba([42, 1]);
  applyLUT(identityLUT(), untouched);
  assert.deepEqual([...untouched], [42, 42, 42, 255]);
});
