package v1alpha1

import (
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
)

// ImagePromotionConverter converts between v1alpha1 API types and domain types for ImagePromotion resources.
type ImagePromotionConverter interface {
	ToDomain(api.ImagePromotion) domain.ImagePromotion
	FromDomain(*domain.ImagePromotion) *api.ImagePromotion
	ListFromDomain(*domain.ImagePromotionList) *api.ImagePromotionList
	ListParamsToDomain(api.ListImagePromotionsParams) domain.ListImagePromotionsParams
}

type imagePromotionConverter struct{}

// NewImagePromotionConverter creates a new ImagePromotionConverter.
func NewImagePromotionConverter() ImagePromotionConverter {
	return &imagePromotionConverter{}
}

func (c *imagePromotionConverter) ToDomain(pub api.ImagePromotion) domain.ImagePromotion {
	return pub
}

func (c *imagePromotionConverter) FromDomain(pub *domain.ImagePromotion) *api.ImagePromotion {
	return pub
}

func (c *imagePromotionConverter) ListFromDomain(l *domain.ImagePromotionList) *api.ImagePromotionList {
	return l
}

func (c *imagePromotionConverter) ListParamsToDomain(p api.ListImagePromotionsParams) domain.ListImagePromotionsParams {
	return p
}
