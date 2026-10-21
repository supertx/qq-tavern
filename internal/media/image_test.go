package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"math/rand"
	"testing"
)

func encoded(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestLargeDimensionsAndOutputBytesAdapt(t *testing.T) {
	input := encoded(t, image.NewNRGBA(image.Rect(0, 0, 4096, 1024)))
	data, mime, err := PrepareEdit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 2048 || cfg.Height != 512 || mime != "image/png" {
		t.Fatal(cfg, mime, err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 1800, 1000))
	rand.New(rand.NewSource(7)).Read(img.Pix)
	input = encoded(t, img)
	if len(input) <= MaxSendBytes || len(input) > MaxInputBytes {
		t.Fatal("bad test fixture", len(input))
	}
	data, mime, err = PrepareSend(context.Background(), input)
	if err != nil || len(data) > MaxSendBytes {
		t.Fatal(len(data), mime, err)
	}
	cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width >= 1800 || cfg.Height >= 1000 {
		t.Fatal(cfg, err)
	}
}
func TestImageLimitsAndCancellation(t *testing.T) {
	input := encoded(t, image.NewNRGBA(image.Rect(0, 0, 4, 2)))
	data, mime, err := PrepareEdit(context.Background(), input)
	if err != nil || !bytes.Equal(data, input) || mime != "image/png" {
		t.Fatal(mime, err)
	}
	bomb := append([]byte(nil), input...)
	binary.BigEndian.PutUint32(bomb[16:20], 40000)
	binary.BigEndian.PutUint32(bomb[20:24], 40000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	for _, raw := range [][]byte{[]byte("not-image"), make([]byte, MaxInputBytes+1), bomb} {
		if _, _, err = PrepareEdit(context.Background(), raw); err == nil {
			t.Fatal("invalid/oversized image accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = PrepareSend(ctx, input); err == nil {
		t.Fatal("cancel ignored")
	}
}
