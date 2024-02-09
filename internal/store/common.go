package store

import (
	"log"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func BuildBaseListQuery(db *gorm.DB, orgId uuid.UUID, labels map[string]string) *gorm.DB {
	query := db.Where("org_id = ?", orgId).Order("name")
	query = LabelSelectionQuery(query, labels)
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
