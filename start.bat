@echo off
title The Daily Star AI Chatbot
echo ===================================================
echo   Starting The Daily Star AI Chatbot on port 8085
echo ===================================================
taskkill /F /IM dailystar-chatbot.exe 2>nul
go run main.go
pause
