package store

import (
	"log"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func BuildBaseListQuery(db *gorm.DB, orgId uuid.UUID, listParams ListParams) *gorm.DB {
	query := db.Where("org_id = ?", orgId).Order("name")
	invertLabels := false
	if listParams.InvertLabels != nil && *listParams.InvertLabels {
		invertLabels = true
	}
	query = LabelSelectionQuery(query, listParams.Labels, invertLabels)
	if listParams.Owner != nil {
		query = db.Where("owner = ?", *listParams.Owner)
	}
	return query
}

func AddPaginationToQuery(query *gorm.DB, limit int, cont *Continue) *gorm.DB {
	query = query.Limit(limit)
	if cont != nil {
		query = query.Where("name >= ?", cont.Name)
	}

	return query
}

func CountRemainingItems(query *gorm.DB, lastItemName string) int64 {
	var count int64
	result := query.Where("name >= ?", lastItemName).Count(&count)
	log.Printf("db.Count(): %d rows affected, error is %v", result.RowsAffected, result.Error)
	return count
}
