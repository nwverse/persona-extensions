# AMap (高德地图)

Places, routes, distance matrix, districts, input tips, interactive map views (`ComponentMap` in chat `tool_views` and UI v12 `nav_page`, with `ui.image` fallback for older clients and live traffic overlays), static map PNGs, saved/fixed places and weather in China for Persona — as eleven read-only assistant tools and an in-app「高德地图」page (`nav_page`) and usage overview card (`overview_card`), powered by the AMap Web Service API with your own key. It can configure and save fixed places (`家` / `home`, `公司` / `company`, and custom places in Extension Settings or the「常用地点」tab) for direct assistant questions like「现在回家要多久」and one-tap commute routing, search places and autocomplete names, show what is nearby (distance, phone, opening hours, rating, cost), turn an address into coordinates and back, look up Chinese administrative districts, plan driving (with waypoints), walking, cycling, e-bike and public transport routes with real decoded polylines (distance, time, tolls, taxi fare estimate, lines and stops), compare distances and travel times from multiple origins to a destination, render static map PNGs (in assistant replies and in-app via `ui.image`, with markers, paths and live traffic), and read a Chinese city's or saved place's weather. Coordinates in and out are WGS-84, like Persona's phone location and saved places. Answers are cached and paced, and a used-up allowance pauses its group and says so.

- [Extension guide, key and quota](../../docs/extensions/amap.md)
- [Installation](../../docs/installation.md)
- [Development and tests](../../docs/development.md)
- [Manifest](persona-extension.json)

```sh
go -C extensions/amap test -race -count=1 ./...
scripts/package.sh amap
```

The tests run against a fake AMap (`fake_test.go`) with scrubbed recordings of public landmarks and need no network or key.
