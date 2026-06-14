#!/bin/bash
# WhatPilot – Run once on a fresh Contabo VPS (Ubuntu 22.04)
set -e

echo "==> Installing system packages..."
apt-get update -y
apt-get install -y golang-go gcc nginx certbot python3-certbot-nginx ufw

echo "==> Configuring firewall..."
ufw allow 22    # SSH
ufw allow 80    # HTTP  (certbot)
ufw allow 443   # HTTPS
ufw --force enable

echo "==> Copying binary and config..."
mkdir -p /opt/whatpilot-backend/data
cp whatpilot /opt/whatpilot-backend/
cp .env /opt/whatpilot-backend/
chown -R www-data:www-data /opt/whatpilot-backend

echo "==> Installing systemd service..."
cp deploy/whatpilot.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable whatpilot
systemctl start whatpilot

echo "==> Installing nginx config..."
cp deploy/nginx.conf /etc/nginx/sites-available/whatpilot
ln -sf /etc/nginx/sites-available/whatpilot /etc/nginx/sites-enabled/
nginx -t && systemctl reload nginx

echo ""
echo "==> Getting SSL certificate..."
echo "    Run: certbot --nginx -d api.your-domain.com"
echo ""
echo "✅ WhatPilot backend deployed!"
echo "   Check status: systemctl status whatpilot"
