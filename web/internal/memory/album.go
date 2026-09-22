package memory

import (
	"database/sql"
	"fmt"
	"strings"

	"myapp/internal/db"
)

type AlbumPhoto struct {
	ID         int64  `json:"id"`
	Uploader   string `json:"uploader"`
	FilePath   string `json:"-"`
	ThumbPath  string `json:"-"`
	FullURL    string `json:"full_url"`
	ThumbURL   string `json:"thumb_url"`
	FileSize   int64  `json:"file_size"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	MimeType   string `json:"mime_type"`
	Caption    string `json:"caption"`
	IsFavorite bool   `json:"is_favorite"`
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	CreatedAt  string `json:"created_at"`
}

func CreateAlbumPhoto(p AlbumPhoto) (AlbumPhoto, error) {
	result, err := db.DB.Exec(`INSERT INTO album_photos(uploader,file_path,thumb_path,file_size,width,height,mime_type,caption,is_favorite,source_type,source_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, p.Uploader, p.FilePath, p.ThumbPath, p.FileSize, p.Width, p.Height, p.MimeType, p.Caption, albumBoolInt(p.IsFavorite), p.SourceType, p.SourceID)
	if err != nil {
		return AlbumPhoto{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AlbumPhoto{}, err
	}
	return GetAlbumPhoto(id)
}

func GetAlbumPhoto(id int64) (AlbumPhoto, error) {
	return scanAlbumPhoto(db.DB.QueryRow(albumPhotoSelect+` WHERE id=?`, id))
}

func ListAlbumPhotos(page, perPage int, favorite bool, uploader string) ([]AlbumPhoto, int, int64, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	where, args := []string{"1=1"}, []interface{}{}
	if favorite {
		where = append(where, "is_favorite=1")
	}
	if uploader != "" {
		where = append(where, "uploader=?")
		args = append(args, uploader)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM album_photos WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	var used int64
	if err := db.DB.QueryRow(`SELECT COALESCE(SUM(file_size),0) FROM album_photos`).Scan(&used); err != nil {
		return nil, 0, 0, err
	}
	args = append(args, perPage, (page-1)*perPage)
	rows, err := db.DB.Query(albumPhotoSelect+` WHERE `+clause+` ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	photos := []AlbumPhoto{}
	for rows.Next() {
		p, err := scanAlbumPhoto(rows)
		if err != nil {
			return nil, 0, 0, err
		}
		photos = append(photos, p)
	}
	return photos, total, used, rows.Err()
}

func AlbumStats() (int, int64, map[string]int, error) {
	var total int
	var used int64
	if err := db.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(file_size),0) FROM album_photos`).Scan(&total, &used); err != nil {
		return 0, 0, nil, err
	}
	by := map[string]int{"Xane": 0, "Rhys": 0}
	rows, err := db.DB.Query(`SELECT uploader,COUNT(*) FROM album_photos GROUP BY uploader`)
	if err != nil {
		return 0, 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uploader string
		var count int
		if rows.Scan(&uploader, &count) == nil {
			by[uploader] = count
		}
	}
	return total, used, by, rows.Err()
}

func SetAlbumPhotoFavorite(id int64, favorite bool) (AlbumPhoto, error) {
	if _, err := db.DB.Exec(`UPDATE album_photos SET is_favorite=? WHERE id=?`, albumBoolInt(favorite), id); err != nil {
		return AlbumPhoto{}, err
	}
	return GetAlbumPhoto(id)
}

func DeleteAlbumPhoto(id int64, uploader string) (AlbumPhoto, error) {
	p, err := GetAlbumPhoto(id)
	if err != nil {
		return AlbumPhoto{}, err
	}
	if p.Uploader != uploader {
		return AlbumPhoto{}, fmt.Errorf("只能删除自己存入的图片")
	}
	if _, err := db.DB.Exec(`DELETE FROM album_photos WHERE id=?`, id); err != nil {
		return AlbumPhoto{}, err
	}
	return p, nil
}

const albumPhotoSelect = `SELECT id,uploader,file_path,thumb_path,file_size,width,height,mime_type,caption,is_favorite,source_type,source_id,created_at FROM album_photos`

type albumScanner interface{ Scan(...interface{}) error }

func scanAlbumPhoto(row albumScanner) (AlbumPhoto, error) {
	var p AlbumPhoto
	var favorite int
	err := row.Scan(&p.ID, &p.Uploader, &p.FilePath, &p.ThumbPath, &p.FileSize, &p.Width, &p.Height, &p.MimeType, &p.Caption, &favorite, &p.SourceType, &p.SourceID, &p.CreatedAt)
	if err != nil {
		return AlbumPhoto{}, err
	}
	p.IsFavorite = favorite != 0
	p.FullURL = "/uploads/" + strings.TrimPrefix(p.FilePath, "uploads/")
	p.ThumbURL = "/uploads/" + strings.TrimPrefix(p.ThumbPath, "uploads/")
	return p, nil
}
func albumBoolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

var _ sql.Scanner
