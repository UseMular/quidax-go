-include env.MAK
export

COMPOSE := $(shell if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version --short 2>/dev/null | grep -Eq '^v?2\.'; then echo "docker-compose"; fi)

.PHONY: start stop status logs config
start:
ifeq ($(strip $(COMPOSE)),)
$(error Docker Compose v2 is required for the application)
endif
	@$(COMPOSE) up --build -d

stop:
	@$(COMPOSE) down

status:
	@$(COMPOSE) ps

logs:
	@$(COMPOSE) logs -f

config:
	@$(COMPOSE) config

.PHONY: run
run:
	@go run .

tunnel:
	ssh -R 80:localhost:55060 localhost.run
