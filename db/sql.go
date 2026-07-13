package db

import (
	"database/sql"
	"sync"

	"github.com/2HgO/quidax-go/config"
	"github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
)

var dataDb *sql.DB
var dataDBOnce = &sync.Once{}

type dbLogger struct {
	log *zap.Logger
}

func (d *dbLogger) Print(v ...any) {
	d.log.Sugar().Info(v...)
}

func GetDataDBConnection(log *zap.Logger) *sql.DB {
	log.Sugar().Info()
	dataDBOnce.Do(func() {
		cfg := mysql.Config{
			User:      config.DATA_DB_USER,
			Passwd:    config.DATA_DB_PASSWORD,
			Net:       "tcp",
			Addr:      config.DATA_DB_URL,
			DBName:    config.DATA_DB_NAME,
			ParseTime: true,
			Logger:    &dbLogger{log: log},
		}
		// Get a database handle.
		var err error
		dataDb, err = sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			log.Sugar().Fatalln(err)
		}

		pingErr := dataDb.Ping()
		if pingErr != nil {
			log.Sugar().Fatalln(pingErr)
		}
	})

	return dataDb
}
