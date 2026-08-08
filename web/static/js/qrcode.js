/*!
 * Minimal QR Code encoder (model 2, byte mode).
 *
 * An implementation of ISO/IEC 18004 written for this project rather
 * than copied from a package, so there is no upstream to track and no
 * transitive dependency to audit. Kept here rather than done in
 * Go because the encoder is pure computation: doing it server-side
 * would either add a seventh Go module or hand a private key to the
 * root agent as a command argument, where it would show up in ps.
 *
 * Scope is deliberately narrow. It encodes a byte string at versions
 * 1-40 with error correction level L or M, which covers a WireGuard
 * config and an OpenVPN profile. No kanji mode, no structured append,
 * no micro QR.
 *
 * License: MIT
 * SPDX-License-Identifier: MIT
 */
(function (global) {
    'use strict';

    // GF(256) tables for Reed-Solomon, primitive polynomial 0x11d.
    var EXP = new Uint8Array(512);
    var LOG = new Uint8Array(256);
    (function () {
        var x = 1;
        for (var i = 0; i < 255; i++) {
            EXP[i] = x;
            LOG[x] = i;
            x <<= 1;
            if (x & 0x100) x ^= 0x11d;
        }
        for (var j = 255; j < 512; j++) EXP[j] = EXP[j - 255];
    })();

    function gfMul(a, b) {
        if (a === 0 || b === 0) return 0;
        return EXP[LOG[a] + LOG[b]];
    }

    // Generator polynomial for `degree` error correction codewords.
    function rsGenerator(degree) {
        var poly = [1];
        for (var d = 0; d < degree; d++) {
            var next = new Array(poly.length + 1).fill(0);
            for (var i = 0; i < poly.length; i++) {
                next[i] ^= poly[i];
                next[i + 1] ^= gfMul(poly[i], EXP[d]);
            }
            poly = next;
        }
        return poly;
    }

    function rsEncode(data, ecLen) {
        var gen = rsGenerator(ecLen);
        var res = new Array(ecLen).fill(0);
        for (var i = 0; i < data.length; i++) {
            var factor = data[i] ^ res[0];
            res.shift();
            res.push(0);
            for (var j = 0; j < ecLen; j++) {
                res[j] ^= gfMul(gen[j + 1], factor);
            }
        }
        return res;
    }

    // Total codewords per version, and the EC block layout for levels
    // L and M. Index is version - 1.
    var TOTAL_CODEWORDS = [
        26, 44, 70, 100, 134, 172, 196, 242, 292, 346, 404, 466, 532, 581, 655,
        733, 815, 901, 991, 1085, 1156, 1258, 1364, 1474, 1588, 1706, 1828, 1921,
        2051, 2185, 2323, 2465, 2611, 2761, 2876, 3034, 3196, 3362, 3532, 3706
    ];

    // [ecCodewordsPerBlock, group1Blocks, group2Blocks] per version.
    var EC_L = [
        [7,1,0],[10,1,0],[15,1,0],[20,1,0],[26,1,0],[18,2,0],[20,2,0],[24,2,0],
        [30,2,0],[18,2,2],[20,4,0],[24,2,2],[26,4,0],[30,3,1],[22,5,1],[24,5,1],
        [28,1,5],[30,5,1],[28,3,4],[28,3,5],[28,4,4],[28,2,7],[30,4,5],[30,6,4],
        [26,8,4],[28,10,2],[30,8,4],[30,3,10],[30,7,7],[30,5,10],[30,13,3],
        [30,17,0],[30,17,1],[30,13,6],[30,12,7],[30,6,14],[30,17,4],[30,4,18],
        [30,20,4],[30,19,6]
    ];
    var EC_M = [
        [10,1,0],[16,1,0],[26,1,0],[18,2,0],[24,2,0],[16,4,0],[18,4,0],[22,2,2],
        [22,3,2],[26,4,1],[30,1,4],[22,6,2],[22,8,1],[24,4,5],[24,5,5],[28,7,3],
        [28,10,1],[26,9,4],[26,3,11],[26,3,13],[26,17,0],[28,17,0],[28,4,14],
        [28,6,14],[28,8,13],[28,19,4],[28,22,3],[28,3,23],[28,21,7],[28,19,10],
        [28,2,29],[28,10,23],[28,14,21],[28,14,23],[28,12,26],[28,6,34],
        [28,29,14],[28,13,32],[28,40,7],[28,18,31]
    ];

    var ALIGNMENT = [
        [], [6,18], [6,22], [6,26], [6,30], [6,34], [6,22,38], [6,24,42],
        [6,26,46], [6,28,50], [6,30,54], [6,32,58], [6,34,62], [6,26,46,66],
        [6,26,48,70], [6,26,50,74], [6,30,54,78], [6,30,56,82], [6,30,58,86],
        [6,34,62,90], [6,28,50,72,94], [6,26,50,74,98], [6,30,54,78,102],
        [6,28,54,80,106], [6,32,58,84,110], [6,30,58,86,114], [6,34,62,90,118],
        [6,26,50,74,98,122], [6,30,54,78,102,126], [6,26,52,78,104,130],
        [6,30,56,82,108,134], [6,34,60,86,112,138], [6,30,58,86,114,142],
        [6,34,62,90,118,146], [6,30,54,78,102,126,150], [6,24,50,76,102,128,154],
        [6,28,54,80,106,132,158], [6,32,58,84,110,136,162],
        [6,26,54,82,110,138,166], [6,30,58,86,114,142,170]
    ];

    function ecTable(level) {
        return level === 'M' ? EC_M : EC_L;
    }

    // Data capacity in bits, after subtracting the EC codewords.
    function dataCodewords(version, level) {
        var spec = ecTable(level)[version - 1];
        var blocks = spec[1] + spec[2];
        return TOTAL_CODEWORDS[version - 1] - spec[0] * blocks;
    }

    function charCountBits(version) {
        return version < 10 ? 8 : 16;
    }

    function chooseVersion(byteLen, level) {
        for (var v = 1; v <= 40; v++) {
            var needed = 4 + charCountBits(v) + byteLen * 8;
            if (dataCodewords(v, level) * 8 >= needed) return v;
        }
        throw new Error('content too long for a QR code');
    }

    function toBytes(text) {
        // TextEncoder is available in every browser that runs the rest
        // of this UI, and hand-rolling UTF-8 here would be one more
        // thing to get wrong.
        return Array.from(new TextEncoder().encode(text));
    }

    function buildCodewords(bytes, version, level) {
        var capacity = dataCodewords(version, level);
        var bits = [];

        function push(value, len) {
            for (var i = len - 1; i >= 0; i--) bits.push((value >> i) & 1);
        }

        push(0x4, 4); // byte mode
        push(bytes.length, charCountBits(version));
        for (var i = 0; i < bytes.length; i++) push(bytes[i], 8);

        // Terminator, then pad to a byte boundary.
        var remaining = capacity * 8 - bits.length;
        push(0, Math.min(4, remaining));
        while (bits.length % 8 !== 0) bits.push(0);

        var words = [];
        for (var b = 0; b < bits.length; b += 8) {
            var v = 0;
            for (var k = 0; k < 8; k++) v = (v << 1) | bits[b + k];
            words.push(v);
        }
        // Alternating pad codewords, per the spec.
        var pads = [0xec, 0x11];
        for (var p = 0; words.length < capacity; p++) words.push(pads[p % 2]);
        return words;
    }

    // Split into blocks, compute EC per block, then interleave.
    function interleave(words, version, level) {
        var spec = ecTable(level)[version - 1];
        var ecLen = spec[0];
        var g1 = spec[1];
        var g2 = spec[2];
        var totalBlocks = g1 + g2;
        var shortLen = Math.floor(words.length / totalBlocks);

        var dataBlocks = [];
        var ecBlocks = [];
        var offset = 0;
        for (var i = 0; i < totalBlocks; i++) {
            var len = i < g1 ? shortLen : shortLen + 1;
            var block = words.slice(offset, offset + len);
            offset += len;
            dataBlocks.push(block);
            ecBlocks.push(rsEncode(block, ecLen));
        }

        var out = [];
        var maxData = shortLen + (g2 > 0 ? 1 : 0);
        for (var c = 0; c < maxData; c++) {
            for (var b = 0; b < totalBlocks; b++) {
                if (c < dataBlocks[b].length) out.push(dataBlocks[b][c]);
            }
        }
        for (var e = 0; e < ecLen; e++) {
            for (var b2 = 0; b2 < totalBlocks; b2++) out.push(ecBlocks[b2][e]);
        }
        return out;
    }

    function newMatrix(size) {
        var m = [];
        for (var i = 0; i < size; i++) m.push(new Array(size).fill(null));
        return m;
    }

    function placeFinder(m, row, col) {
        for (var r = -1; r <= 7; r++) {
            for (var c = -1; c <= 7; c++) {
                var rr = row + r;
                var cc = col + c;
                if (rr < 0 || rr >= m.length || cc < 0 || cc >= m.length) continue;
                var inRing = (r >= 0 && r <= 6 && (c === 0 || c === 6)) ||
                             (c >= 0 && c <= 6 && (r === 0 || r === 6));
                var inCore = r >= 2 && r <= 4 && c >= 2 && c <= 4;
                m[rr][cc] = inRing || inCore ? 1 : 0;
            }
        }
    }

    function placeFunctionPatterns(m, version) {
        var size = m.length;
        placeFinder(m, 0, 0);
        placeFinder(m, 0, size - 7);
        placeFinder(m, size - 7, 0);

        // Timing patterns.
        for (var i = 8; i < size - 8; i++) {
            var bit = i % 2 === 0 ? 1 : 0;
            if (m[6][i] === null) m[6][i] = bit;
            if (m[i][6] === null) m[i][6] = bit;
        }

        // Alignment patterns, skipping the ones that collide with the
        // finders.
        var centers = ALIGNMENT[version - 1];
        for (var a = 0; a < centers.length; a++) {
            for (var b = 0; b < centers.length; b++) {
                var cr = centers[a];
                var cc2 = centers[b];
                if ((cr <= 8 && cc2 <= 8) ||
                    (cr <= 8 && cc2 >= size - 9) ||
                    (cr >= size - 9 && cc2 <= 8)) continue;
                for (var dr = -2; dr <= 2; dr++) {
                    for (var dc = -2; dc <= 2; dc++) {
                        var edge = Math.max(Math.abs(dr), Math.abs(dc));
                        m[cr + dr][cc2 + dc] = edge !== 1 ? 1 : 0;
                    }
                }
            }
        }

        // Dark module.
        m[size - 8][8] = 1;
    }

    function reserveFormatAreas(m) {
        var size = m.length;
        for (var i = 0; i < 9; i++) {
            if (m[8][i] === null) m[8][i] = 0;
            if (m[i][8] === null) m[i][8] = 0;
        }
        for (var j = 0; j < 8; j++) {
            if (m[8][size - 1 - j] === null) m[8][size - 1 - j] = 0;
            if (m[size - 1 - j][8] === null) m[size - 1 - j][8] = 0;
        }
    }

    function reserveVersionAreas(m, version) {
        if (version < 7) return;
        var size = m.length;
        for (var i = 0; i < 6; i++) {
            for (var j = 0; j < 3; j++) {
                m[i][size - 11 + j] = 0;
                m[size - 11 + j][i] = 0;
            }
        }
    }

    function placeData(m, codewords) {
        var size = m.length;
        var bitIndex = 0;
        var total = codewords.length * 8;

        function nextBit() {
            if (bitIndex >= total) return 0;
            var word = codewords[bitIndex >> 3];
            var bit = (word >> (7 - (bitIndex & 7))) & 1;
            bitIndex++;
            return bit;
        }

        var upward = true;
        for (var col = size - 1; col > 0; col -= 2) {
            if (col === 6) col--; // skip the vertical timing column
            for (var i = 0; i < size; i++) {
                var row = upward ? size - 1 - i : i;
                for (var c = 0; c < 2; c++) {
                    var cc = col - c;
                    if (m[row][cc] !== null) continue;
                    m[row][cc] = nextBit();
                }
            }
            upward = !upward;
        }
    }

    var MASKS = [
        function (r, c) { return (r + c) % 2 === 0; },
        function (r) { return r % 2 === 0; },
        function (r, c) { return c % 3 === 0; },
        function (r, c) { return (r + c) % 3 === 0; },
        function (r, c) { return (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0; },
        function (r, c) { return ((r * c) % 2) + ((r * c) % 3) === 0; },
        function (r, c) { return (((r * c) % 2) + ((r * c) % 3)) % 2 === 0; },
        function (r, c) { return (((r + c) % 2) + ((r * c) % 3)) % 2 === 0; }
    ];

    function isFunctionModule(version, size, r, c) {
        if (r === 6 || c === 6) return true;
        if (r < 9 && c < 9) return true;
        if (r < 9 && c >= size - 8) return true;
        if (r >= size - 8 && c < 9) return true;
        if (version >= 7) {
            if (r < 6 && c >= size - 11) return true;
            if (c < 6 && r >= size - 11) return true;
        }
        var centers = ALIGNMENT[version - 1];
        for (var a = 0; a < centers.length; a++) {
            for (var b = 0; b < centers.length; b++) {
                var cr = centers[a];
                var cc = centers[b];
                if ((cr <= 8 && cc <= 8) ||
                    (cr <= 8 && cc >= size - 9) ||
                    (cr >= size - 9 && cc <= 8)) continue;
                if (Math.abs(r - cr) <= 2 && Math.abs(c - cc) <= 2) return true;
            }
        }
        return false;
    }

    function applyMask(m, version, maskIndex) {
        var size = m.length;
        var fn = MASKS[maskIndex];
        for (var r = 0; r < size; r++) {
            for (var c = 0; c < size; c++) {
                if (isFunctionModule(version, size, r, c)) continue;
                if (fn(r, c)) m[r][c] ^= 1;
            }
        }
    }

    // BCH(15,5) format information, per the spec.
    function formatBits(level, maskIndex) {
        var levelBits = level === 'M' ? 0 : 1; // L = 01, M = 00
        var data = (levelBits << 3) | maskIndex;
        var rem = data;
        for (var i = 0; i < 10; i++) rem = (rem << 1) ^ (((rem >> 9) & 1) * 0x537);
        var bits = ((data << 10) | rem) ^ 0x5412;
        return bits;
    }

    function placeFormat(m, level, maskIndex) {
        var size = m.length;
        var bits = formatBits(level, maskIndex);
        for (var i = 0; i < 15; i++) {
            // Most significant bit first. Writing LSB first produces a
            // symbol that is internally consistent, so reading it back
            // with the same mapping looks correct, and no decoder can
            // read it because the format tells them the wrong mask.
            var bit = (bits >> (14 - i)) & 1;

            // Copy 1: along row 8, then up column 8, skipping the
            // timing row.
            if (i < 6) m[8][i] = bit;
            else if (i === 6) m[8][7] = bit;
            else if (i === 7) m[8][8] = bit;
            else if (i === 8) m[7][8] = bit;
            else m[14 - i][8] = bit;

            // Copy 2: up column 8 from the bottom, then along row 8.
            // Seven cells, not eight: the eighth is the dark module,
            // and writing a format bit over it corrupts both.
            if (i < 7) m[size - 1 - i][8] = bit;
            else m[8][size - 15 + i] = bit;
        }
    }

    // BCH(18,6) version information, versions 7 and up.
    function placeVersion(m, version) {
        if (version < 7) return;
        var size = m.length;
        var rem = version;
        for (var i = 0; i < 12; i++) rem = (rem << 1) ^ (((rem >> 11) & 1) * 0x1f25);
        var bits = (version << 12) | rem;
        for (var j = 0; j < 18; j++) {
            var bit = (bits >> j) & 1;
            var r = Math.floor(j / 3);
            var c = j % 3;
            m[r][size - 11 + c] = bit;
            m[size - 11 + c][r] = bit;
        }
    }

    // Penalty scoring, used to pick the mask that reads most reliably.
    function penalty(m) {
        var size = m.length;
        var score = 0;

        function runPenalty(run) {
            return run >= 5 ? 3 + (run - 5) : 0;
        }

        for (var r = 0; r < size; r++) {
            var runH = 1, runV = 1;
            for (var c = 1; c < size; c++) {
                runH = m[r][c] === m[r][c - 1] ? runH + 1 : (score += runPenalty(runH), 1);
                runV = m[c][r] === m[c - 1][r] ? runV + 1 : (score += runPenalty(runV), 1);
            }
            score += runPenalty(runH) + runPenalty(runV);
        }

        for (var r2 = 0; r2 < size - 1; r2++) {
            for (var c2 = 0; c2 < size - 1; c2++) {
                var v = m[r2][c2];
                if (v === m[r2][c2 + 1] && v === m[r2 + 1][c2] && v === m[r2 + 1][c2 + 1]) score += 3;
            }
        }

        var dark = 0;
        for (var r3 = 0; r3 < size; r3++) {
            for (var c3 = 0; c3 < size; c3++) dark += m[r3][c3];
        }
        var pct = (dark * 100) / (size * size);
        score += Math.floor(Math.abs(pct - 50) / 5) * 10;

        return score;
    }

    function encode(text, level) {
        level = level === 'M' ? 'M' : 'L';
        var bytes = toBytes(text);
        var version = chooseVersion(bytes.length, level);
        var words = interleave(buildCodewords(bytes, version, level), version, level);

        var size = version * 4 + 17;
        var best = null;
        var bestScore = Infinity;

        for (var mask = 0; mask < 8; mask++) {
            var m = newMatrix(size);
            placeFunctionPatterns(m, version);
            reserveFormatAreas(m);
            reserveVersionAreas(m, version);
            placeData(m, words);
            applyMask(m, version, mask);
            placeFormat(m, level, mask);
            placeVersion(m, version);

            var s = penalty(m);
            if (s < bestScore) {
                bestScore = s;
                best = m;
            }
        }
        return best;
    }

    // Draws onto a canvas. A canvas rather than markup on purpose: it
    // is not an HTML sink, so content carrying a private key never
    // passes through innerHTML on its way to the screen.
    function render(canvas, text, options) {
        options = options || {};
        var matrix = encode(text, options.level);
        var size = matrix.length;
        var quiet = options.quiet == null ? 4 : options.quiet;
        var scale = options.scale || 4;
        var dim = (size + quiet * 2) * scale;

        canvas.width = dim;
        canvas.height = dim;

        var ctx = canvas.getContext('2d');
        ctx.fillStyle = '#ffffff';
        ctx.fillRect(0, 0, dim, dim);
        ctx.fillStyle = '#000000';
        for (var r = 0; r < size; r++) {
            for (var c = 0; c < size; c++) {
                if (matrix[r][c]) {
                    ctx.fillRect((c + quiet) * scale, (r + quiet) * scale, scale, scale);
                }
            }
        }
        return matrix;
    }

    global.QRCode = { encode: encode, render: render };
})(window);
