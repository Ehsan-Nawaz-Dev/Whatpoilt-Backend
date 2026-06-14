#!/bin/bash
# WhatPilot Backend — dependency installer
set -e

echo "==> Installing WhatPilot backend dependencies..."

go get github.com/gin-gonic/gin@latest
go get github.com/gin-contrib/cors@latest
go get github.com/google/uuid@latest
go get github.com/joho/godotenv@latest
go get github.com/mattn/go-sqlite3@latest
go get github.com/skip2/go-qrcode@latest
go get go.mau.fi/whatsmeow@latest
go get google.golang.org/protobuf@latest

echo "==> Running go mod tidy..."
go mod tidy

echo "==> Creating data directories..."
mkdir -p data/sessions

echo ""
echo "✅ WhatPilot backend ready!"
echo "   Copy .env.example to .env, fill in values, then run:"
echo "   go run main.go"
echo ""
echo "   Or build the binary:"
echo "   go build -o whatpilot ."
