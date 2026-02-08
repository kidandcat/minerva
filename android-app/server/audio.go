package main

// PCM audio utilities for phone bridge ↔ Gemini audio conversion.
//
// Phone Bridge APK: PCM 16kHz 16-bit signed LE mono
// Gemini input:     PCM 16kHz 16-bit signed LE mono
// Gemini output:    PCM 24kHz 16-bit signed LE mono

// resampleLinear resamples PCM samples using linear interpolation.
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

// resample24to16 resamples 24kHz PCM bytes to 16kHz PCM bytes.
func resample24to16(input []byte) []byte {
	samples := bytesToPCM(input)
	resampled := resampleLinear(samples, 24000, 16000)
	return pcmToBytes(resampled)
}
