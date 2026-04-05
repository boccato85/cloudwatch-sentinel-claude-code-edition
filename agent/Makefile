BINARY  := sentinel-agent
SERVICE := sentinel-claude

.PHONY: build start stop restart status logs open clean

build:
	go build -o $(BINARY) .

start:
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
