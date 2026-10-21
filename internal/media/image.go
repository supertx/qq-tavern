// Package media adapts image inputs and outputs to bounded transport budgets.
package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const MaxInputBytes = 10 << 20
const MaxSendBytes = 5 << 20
const MaxPixels = 40_000_000

// Only one full image is decoded at once, including across concurrent turns.
var decodeSlot = make(chan struct{}, 1)

func PrepareEdit(ctx context.Context, data []byte) ([]byte, string, error) {
	return prepare(ctx, data, 2048, 8<<20, true)
}

func PrepareSend(ctx context.Context, data []byte) ([]byte, string, error) {
	return prepare(ctx, data, 0, MaxSendBytes, false)
}

func prepare(ctx context.Context, data []byte, maxEdge, maxBytes int, flattenGIF bool) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if len(data) > MaxInputBytes {
		return nil, "", errors.New("图片超过 10 MiB，请先缩小原图")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("图片无效或格式不支持，请使用 PNG、JPEG、WebP 或 GIF")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > MaxPixels/int64(cfg.Height) {
		return nil, "", errors.New("图片超过 4000 万像素，请先缩小原图")
	}
	mime := "image/" + format
	if len(data) <= maxBytes && (maxEdge == 0 || max(cfg.Width, cfg.Height) <= maxEdge) && !(format == "gif" && flattenGIF) {
		return data, mime, nil
	}
	select {
	case decodeSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	defer func() { <-decodeSlot }()
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("图片解码失败")
	}
	w, h := cfg.Width, cfg.Height
	if maxEdge == 0 {
		maxEdge = 2048
	}
	if edge := max(w, h); edge > maxEdge {
		w = max(1, w*maxEdge/edge)
		h = max(1, h*maxEdge/edge)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
		var buf bytes.Buffer
		if err = png.Encode(&buf, dst); err != nil {
			return nil, "", err
		}
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if buf.Len() <= maxBytes {
			return buf.Bytes(), "image/png", nil
		}
		if w == 1 && h == 1 {
			return nil, "", errors.New("图片无法适配上传限制")
		}
		w, h = max(1, w*3/4), max(1, h*3/4)
	}
}
