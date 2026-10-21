package worker

import (
	"context"
	"encoding/base64"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/media"
	"github.com/supertx/qq-tavern/internal/model"
	"github.com/supertx/qq-tavern/internal/netguard"
)

func (s *ToolSession) fetchImage(ctx context.Context, raw string) ([]byte, string, error) {
	if fetcher, ok := s.tools.sender.(interface {
		FetchMedia(context.Context, domain.Event, string) ([]byte, string, error)
	}); ok {
		return fetcher.FetchMedia(ctx, s.event, raw)
	}
	return netguard.Fetch(ctx, s.tools.public, raw, media.MaxInputBytes)
}

// Only the live request contains image bytes. Expiring QQ URLs and large inline
// data must not be replayed to the provider from historical turns.
func (s *ToolSession) visionInput(ctx context.Context, input model.Message) model.Message {
	input.Images = nil
	original := userMessage(s.event)
	images := original.Images
	if len(images) > 5 {
		images = images[:5]
		input.Content += "\n[本轮只处理前5张图片，其余请分批发送。]"
	}
	for _, raw := range images {
		data, _, err := s.fetchImage(ctx, raw)
		if err == nil {
			var mime string
			data, mime, err = media.PrepareEdit(ctx, data)
			if err == nil {
				input.Images = append(input.Images, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data))
				continue
			}
		}
		input.Content += "\n[一张图片未能读取，可能已过期、格式无效或过大；请勿猜测图片内容，可请用户重新发送原图。]"
	}
	return input
}

func withoutImageData(messages []model.Message) []model.Message {
	out := append([]model.Message(nil), messages...)
	for i := range out {
		if len(out[i].Images) > 0 {
			out[i].Images = nil
			out[i].Content += "\n[历史图片未重新加载；如需查看或改图，请在本轮引用或发送原图。]"
		}
	}
	return out
}
