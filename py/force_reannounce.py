import time
from qbittorrentapi import Client

# 配置 qBittorrent 客户端连接信息
qb = Client(host='http://192.168.0.108:8080', username='castle', password='abcdefg123456')
# exit(1)

# 确保连接成功
try:
    qb.auth_log_in()
except Exception as e:
    print(f"连接失败: {e}")
    exit(1)

# 获取所有 torrents 的 hashes
torrents = qb.torrents_info()
torrent_hashes = [torrent.hash for torrent in torrents]

# 暂停所有 torrents
qb.torrents_pause(torrent_hashes=torrent_hashes)
print("已暂停所有 torrents")

# 等待 3 秒钟
time.sleep(3)

# 强制重新 announce 所有 torrents
qb.torrents_reannounce(torrent_hashes=torrent_hashes)
print("已强制重新 announce 所有 torrents")

# 等待 3 秒钟
time.sleep(3)

# 恢复所有 torrents
qb.torrents_resume(torrent_hashes=torrent_hashes)
print("已恢复所有 torrents")

# 断开连接
qb.auth_log_out()
