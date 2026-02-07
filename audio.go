package main

// G.711 mu-law codec and PCM resampling utilities for Twilio ↔ Gemini audio bridge.
//
// Twilio: mu-law 8kHz 8-bit mono
// Gemini input: PCM 16kHz 16-bit signed LE mono
// Gemini output: PCM 24kHz 16-bit signed LE mono

// mulawDecode converts a single mu-law byte to a 16-bit PCM sample.
func mulawDecode(b byte) int16 {
	// Complement and extract fields
	b = ^b
	sign := int16(b & 0x80)
	exponent := int(b>>4) & 0x07
	mantissa := int(b & 0x0F)

	sample := int16((mantissa<<3 | 0x84) << exponent)
	sample -= 0x84 // bias removal

	if sign != 0 {
		return -sample
	}
	return sample
}

// mulawEncode converts a 16-bit PCM sample to a mu-law byte.
func mulawEncode(sample int16) byte {
	const bias = 0x84
	const clip = 32635

	sign := byte(0)
	if sample < 0 {
		sign = 0x80
		sample = -sample
	}
	if sample > clip {
		sample = clip
	}

	s := int(sample) + bias
	exponent := byte(7)
	for i := byte(0); i < 8; i++ {
		if s < (1 << (i + 8)) {
			exponent = i
			break
		}
	}

	mantissa := byte((s >> (exponent + 3)) & 0x0F)
	return ^(sign | (exponent << 4) | mantissa)
}

// mulawDecodeChunk decodes a slice of mu-law bytes to PCM 16-bit samples.
func mulawDecodeChunk(data []byte) []int16 {
	samples := make([]int16, len(data))
	for i, b := range data {
		samples[i] = mulawDecode(b)
	}
	return samples
}

// mulawEncodeChunk encodes PCM 16-bit samples to mu-law bytes.
func mulawEncodeChunk(samples []int16) []byte {
	data := make([]byte, len(samples))
	for i, s := range samples {
		data[i] = mulawEncode(s)
	}
	return data
}

// resampleLinear resamples PCM samples using linear interpolation.
// Works for both upsampling (e.g. 8kHz→16kHz) and downsampling (e.g. 24kHz→8kHz).
func resampleLinear(input []int16, inRate, outRate int) []int16 {
	if len(input) == 0 || inRate == outRate {
		return input
	}

	outLen := len(input) * outRate / inRate
	if outLen == 0 {
		return nil
	}

	output := make([]int16, outLen)
	ratio := float64(inRate) / float64(outRate)

	for i := range output {
		srcPos := float64(i) * ratio
		srcIdx := int(srcPos)
		frac := srcPos - float64(srcIdx)

		if srcIdx+1 < len(input) {
			output[i] = int16(float64(input[srcIdx])*(1-frac) + float64(input[srcIdx+1])*frac)
		} else if srcIdx < len(input) {
			output[i] = input[srcIdx]
		}
	}

	return output
}

// pcmToBytes converts PCM 16-bit samples to little-endian bytes.
func pcmToBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, s := range samples {
		data[i*2] = byte(s)
		data[i*2+1] = byte(s >> 8)
	}
	return data
}

// bytesToPCM converts little-endian bytes to PCM 16-bit samples.
func bytesToPCM(data []byte) []int16 {
	samples := make([]int16, len(data)/2)
	for i := range samples {
		samples[i] = int16(data[i*2]) | int16(data[i*2+1])<<8
	}
	return samples
}
