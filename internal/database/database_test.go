package database

import "testing"

func TestNewMemoryDSNGivesEachDatabaseItsOwnData(t *testing.T) {
	type row struct {
		ID   uint
		Note string
	}

	first, err := Open(NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening first database: %v", err)
	}
	second, err := Open(NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening second database: %v", err)
	}
	for name, db := range map[string]interface{ AutoMigrate(...any) error }{"first": first, "second": second} {
		if err := db.AutoMigrate(&row{}); err != nil {
			t.Fatalf("migrating %s database: %v", name, err)
		}
	}

	if err := first.Create(&row{Note: "only in the first database"}).Error; err != nil {
		t.Fatalf("writing to first database: %v", err)
	}

	var inFirst, inSecond int64
	first.Model(&row{}).Count(&inFirst)
	second.Model(&row{}).Count(&inSecond)
	if inFirst != 1 || inSecond != 0 {
		t.Errorf("rows: first = %d, second = %d; want 1 and 0", inFirst, inSecond)
	}
}
