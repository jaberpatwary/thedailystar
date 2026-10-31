.PHONY: start build run clean help

# Default command: make start
start:
	@go run main.go

build:
	@go build -o dailystar-chatbot.exe main.go

run: build
	@.\dailystar-chatbot.exe

clean:
	@if exist dailystar-chatbot.exe del /f /q dailystar-chatbot.exe

help:
	@echo Daily Star Chatbot - Available Commands:
	@echo   make start  - Run project directly (go run main.go)
	@echo   make build  - Build executable (dailystar-chatbot.exe)
	@echo   make run    - Build and run the compiled binary
	@echo   make clean  - Clean build artifacts
