### 一个虚假的BT/PT下载端
只要知道服务器 peers的IP和port，以及种子的hash值，就可以对其进行虚假下载

启动命令：
```bash
go run main.go --port=8084
```

支持对qbittorrent服务端进行全体reanncounce，掩耳盗铃般得抹除一些tracker服务器后台统计数据。

