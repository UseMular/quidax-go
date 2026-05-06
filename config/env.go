package config

import "os"

var (
	DATA_DB_URL = os.Getenv("DATA_DB_URL")
	TX_DB_URL   = os.Getenv("TX_DB_URL")
	PORT        = os.Getenv("PORT")
)
