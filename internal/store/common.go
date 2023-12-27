package store

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func BuildBaseListQuery(db *gorm.DB, orgId uuid.UUID, labels map[string]string) *gorm.DB {
	query := db.Where("org_id = ?", orgId).Order("name")
	query = LabelSelectionQuery(query, labels)
	return query
}

func AddPaginationToQuery(query *gorm.DB, limit int, cont string) *gorm.DB {
	if limit > 0 {
		query = query.Limit(limit)
	}
	if cont != "" {
		query = query.Where("name > ?", cont)
	}

	return query
}

func FormatResourcesAfterPagination(query *gorm.DB, limit int, lastItemName string) (*string, *int64) {
	if limit == 0 {
		return nil, nil
	}

	var count int64
	query.Where("name > ?", lastItemName).Count(&count)

	if count == 0 {
		return nil, nil
	}
	return &lastItemName, &count
}
