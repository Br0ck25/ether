package transcode

import "fmt"

// gpuPipeline keeps decoded pictures on the accelerator until the encoder
// consumes them. Merely selecting a hardware encoder does not do this: a
// software scale/format filter downloads every picture and the encoder uploads
// it again. Return false when the installed FFmpeg lacks the complete graph;
// planVideo then retains the software-filter path (including subtitle burn-in).
func gpuPipeline(s runSpec, accel string, w, h int, toneMap, keepHDR, burn bool) (input, filters []string, ok bool) {
	if burn || w <= 0 || h <= 0 {
		return nil, nil, false
	}
	format := "nv12"
	if keepHDR {
		format = "p010le"
	}
	switch accel {
	case AccelNVENC:
		if !s.Features.hwaccels["cuda"] || !s.Features.hasFilter("scale_cuda") || (toneMap && !s.Features.hasFilter("tonemap_cuda")) {
			return nil, nil, false
		}
		input = []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
		if toneMap {
			filters = append(filters, "tonemap_cuda=tonemap=hable:format=nv12:t=bt709:m=bt709:p=bt709")
		}
		filters = append(filters, fmt.Sprintf("scale_cuda=w=%d:h=%d:format=%s", w, h, format))
	case AccelVideoToolbox:
		if !s.Features.hwaccels["videotoolbox"] || !s.Features.hasFilter("scale_vt") || (toneMap && !s.Features.hasFilter("tonemap_videotoolbox")) {
			return nil, nil, false
		}
		input = []string{"-hwaccel", "videotoolbox", "-hwaccel_output_format", "videotoolbox_vld"}
		if toneMap {
			filters = append(filters, "tonemap_videotoolbox=tonemap=hable:format=nv12:t=bt709:m=bt709:p=bt709")
		}
		scale := fmt.Sprintf("scale_vt=w=%d:h=%d", w, h)
		if s.Features.scaleVTFormat {
			scale += ":format=" + format
		} else if v := s.Info.Video(); v != nil && v.BitDepth > 8 && !keepHDR && !toneMap {
			// Older scale_vt can resize but cannot convert 10-bit SDR to
			// the 8-bit surfaces required by the H.264 encoder.
			return nil, nil, false
		}
		filters = append(filters, scale)
	case AccelQSV:
		if !s.Features.hwaccels["qsv"] || !s.Features.hasFilter("vpp_qsv") {
			return nil, nil, false
		}
		// Hardware VPP tone mapping is driver-dependent. Keep the existing
		// software HDR path until the device has been validated for it.
		if toneMap {
			return nil, nil, false
		}
		input = []string{"-hwaccel", "qsv", "-hwaccel_output_format", "qsv"}
		filters = []string{fmt.Sprintf("vpp_qsv=w=%d:h=%d:format=%s", w, h, format)}
	default:
		return nil, nil, false
	}
	return input, filters, true
}
