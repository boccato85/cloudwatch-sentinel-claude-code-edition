BINARY   := sentinel-agent
SERVICE  := sentinel
ENV_FILE := ../.env
ENV_EXAMPLE := ../.env.example

.PHONY: build setup start stop restart status logs open clean

build:
	go build -o $(BINARY) .

setup:
	@if [ ! -f $(ENV_FILE) ]; then \
		cp $(ENV_EXAMPLE) $(ENV_FILE); \
		echo "✅ .env created from .env.example — edit it before running 'make start'"; \
	else \
		echo "ℹ️  .env already exists — skipping"; \
	fi

start: build
	@if [ ! -f $(ENV_FILE) ]; then \
		echo "❌ .env not found. Run 'make setup' first."; exit 1; \
	fi
	systemctl --user daemon-reload
	systemctl --user start $(SERVICE)
	@echo "Sentinel running → http://localhost:8080"

stop:
	systemctl --user stop $(SERVICE)

restart:
	systemctl --user restart $(SERVICE)

status:
	systemctl --user status $(SERVICE)

logs:
	journalctl --user -u $(SERVICE) -f

open:
	xdg-open http://localhost:8080

clean:
	rm -f $(BINARY)
