/*
*

	@author: kiki
	@since: 2025/5/25
	@desc: //TODO

*
*/

package db

import (
	"time"

	"gorm.io/gorm"
)

//type AOD struct {
//	gorm.Model
//	Name       string    `gorm:"type:varchar(30);column:name;unique" json:"name"`
//	Size       int64     `gorm:"column:size;default:0" json:"size"`
//	Lastuse    time.Time `gorm:"type:datetime;column:lastuse" json:"lastuse"`
//	Expiration time.Time `gorm:"type:datetime;column:expiration" json:"expiration"`
//	Content    []byte    `gorm:"type:longtext;column:content" json:"content"`
//}

type Module struct {
	gorm.Model
	CRC64      uint64    `gorm:"column:crc64;not null"`
	Name       string    `gorm:"type:varchar(255);column:name;unique" json:"name"`
	Size       int64     `gorm:"column:size;default:0" json:"size"`
	Lastuse    time.Time `gorm:"type:datetime;column:lastuse" json:"lastuse"`
	Expiration time.Time `gorm:"type:datetime;column:expiration" json:"expiration"`
}

type Client struct {
	gorm.Model
	Serveraddress    string         `gorm:"type:varchar(30);column:serveraddress;unique" json:"serveraddress"`
	Maxretentiondays int64          `gorm:"column:maxretentiondays" json:"maxretentiondays"`
	Reload           bool           `gorm:"column:reload" json:"reload"`
	Store            []Clientmodule `gorm:"foreignkey:StoreID" json:"StoreID"`
}

type Clientmodule struct {
	gorm.Model
	StoreID    uint
	Partnumber string    `gorm:"type:varchar(255);column:partnumber" json:"partnumber"`
	Name       string    `gorm:"type:varchar(255);column:name" json:"name"`
	Expiration time.Time `gorm:"type:datetime;column:expiration" json:"expiration"`
}

func AutoMigrate() []any {
	return []any{&Module{}, &Client{}, &Clientmodule{}}
}
