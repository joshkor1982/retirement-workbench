// Neighborhood map on the Housing page. Leaflet draws it; the app's /map/*
// endpoints supply the data (see mapdata.go). Layers load only when switched
// on, and only for the area in view.
(function () {
  'use strict';
  var el = document.getElementById('hood-map');
  if (!el || !window.L) return;

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function safeURL(u) { return /^https?:\/\//.test(u || '') ? u : ''; }
  var status = document.getElementById('map-status');
  function say(msg) { if (status) status.textContent = msg || ''; }

  var map = L.map(el, { scrollWheelZoom: false, zoomControl: true });
  // Base maps. OpenStreetMap's volunteer tile servers block requests that
  // carry no Referer, and this app sends none to other sites by default
  // (Referrer-Policy: same-origin), so the tile layer asks for one. USGS's
  // National Map tiles are US government, public domain, and need no key.
  var usgs = function (svc) { return 'https://basemap.nationalmap.gov/arcgis/rest/services/' + svc + '/MapServer/tile/{z}/{y}/{x}'; };
  var bases = {
    streets: { label: 'Streets', url: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png', max: 19, cls: 'osm-base',
      attr: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors' },
    topo: { label: 'Topo', url: usgs('USGSTopo'), max: 16, attr: 'USGS The National Map' },
    satellite: { label: 'Satellite', url: usgs('USGSImageryTopo'), max: 16, attr: 'USGS The National Map: imagery' }
  };
  var base = null;
  function setBase(key) {
    var b = bases[key] || bases.streets;
    if (base) map.removeLayer(base);
    base = L.tileLayer(b.url, { maxZoom: b.max, maxNativeZoom: b.max, className: b.cls || '', attribution: b.attr,
      referrerPolicy: 'strict-origin-when-cross-origin' }).addTo(map);
    base.bringToBack();
  }
  var pick = document.getElementById('map-base');
  if (pick) {
    Object.keys(bases).forEach(function (k) { var o = document.createElement('option'); o.value = k; o.textContent = bases[k].label; pick.appendChild(o); });
    pick.addEventListener('change', function () { setBase(pick.value); });
  }
  var startBase = new URLSearchParams(location.search).get('base');
  if (!bases[startBase]) startBase = 'streets';
  if (pick) pick.value = startBase;
  setBase(startBase);
  el.addEventListener('click', function () { map.scrollWheelZoom.enable(); });

  // FEMA flood zones: FEMA's map-image service, one image per tile.
  map.createPane('flood'); map.getPane('flood').style.zIndex = 350;
  var Flood = L.TileLayer.extend({
    getTileUrl: function (c) {
      var size = 256, nw = L.point(c.x * size, c.y * size), se = nw.add([size, size]);
      var crs = this._map.options.crs, z = c.z;
      var a = crs.project(crs.pointToLatLng(nw, z)), b = crs.project(crs.pointToLatLng(se, z));
      var bbox = [a.x, b.y, b.x, a.y].join(',');
      return 'https://hazards.fema.gov/arcgis/rest/services/public/NFHL/MapServer/export?bbox=' + bbox +
        '&bboxSR=3857&imageSR=3857&size=256,256&format=png32&transparent=true&layers=show:28&f=image';
    }
  });

  var DENS = [[0, '#f7fbff'], [500, '#c6dbef'], [1500, '#6baed6'], [3000, '#2171b5'], [6000, '#08306b']];
  function densColor(d) { var c = DENS[0][1]; DENS.forEach(function (s) { if (d >= s[0]) c = s[1]; }); return c; }

  // Layer definitions. kind: 'bbox' reloads on pan; 'once' loads a single time.
  var defs = {
    homes: { label: 'Homes for Sale', color: '#e8b923', url: function () { return '/map/homes'; }, kind: 'once' },
    flood: { label: 'Flood Zones', tile: true },
    density: { label: 'Population Density', url: function (b) { return '/map/tracts?bbox=' + b; }, kind: 'bbox', max: 1.2, poly: true },
    crime: { label: 'Crime by Police Agency', crime: true },
    schools: { label: 'Public Schools', color: '#7b61ff', url: function (b) { return '/map/schools?bbox=' + b; }, kind: 'bbox', max: 0.8 }
  };
  var PLACES = [
    ['grocery', 'Grocery', '#0ca678'], ['shopping', 'Shopping', '#d9480f'], ['dining', 'Dining', '#c2255c'],
    ['health', 'Hospitals and Clinics', '#e03131'], ['pharmacy', 'Pharmacies', '#f06595'], ['parks', 'Parks', '#94d82d'],
    ['fitness', 'Fitness', '#1098ad'], ['fuel', 'Gas', '#868e96'], ['library', 'Libraries', '#845ef7'],
    ['safety', 'Fire and Police', '#364fc7'], ['childcare', 'Childcare', '#fab005'], ['worship', 'Places of Worship', '#a0795c'],
    ['military', 'Military', '#5c7a29']
  ];
  PLACES.forEach(function (p) {
    defs[p[0]] = { label: p[1], color: p[2], url: function (b) { return '/map/places?kind=' + p[0] + '&bbox=' + b; }, kind: 'bbox', max: 0.5 };
  });

  var layers = {}, loaded = {}, center = null;

  function bboxStr() {
    var b = map.getBounds();
    return [b.getWest(), b.getSouth(), b.getEast(), b.getNorth()].map(function (n) { return n.toFixed(3); }).join(',');
  }
  function span() { var b = map.getBounds(); return Math.max(b.getEast() - b.getWest(), b.getNorth() - b.getSouth()); }

  function popupFor(key, p) {
    if (key === 'homes') {
      var u = safeURL(p.url), ph = safeURL(p.photo);
      return (ph ? '<img class="map-photo" src="' + esc(ph) + '" alt="">' : '') +
        '<b>' + esc(p.price) + '</b><br>' + esc(p.address) + '<br>' + esc(p.beds) + ' bd · ' + esc(p.baths) + ' ba' +
        (p.sqft ? ' · ' + esc(p.sqft) + ' sq ft' : '') + (u ? '<br><a href="' + esc(u) + '" target="_blank" rel="noopener">Open listing ↗</a>' : '');
    }
    return '<b>' + esc(p.name) + '</b>' + (p.what && p.what !== p.name ? '<br>' + esc(p.what) : '') + (p.address ? '<br>' + esc(p.address) : '');
  }

  function drawGeo(key, data) {
    var d = defs[key];
    if (d.poly) {
      return L.geoJSON(data, {
        style: function (f) { return { color: '#555', weight: 0.6, fillColor: densColor(f.properties.density), fillOpacity: 0.55 }; },
        onEachFeature: function (f, l) {
          l.bindPopup('<b>' + esc(f.properties.name) + '</b><br>' + esc(f.properties.density.toLocaleString()) +
            ' people per sq mi<br>' + esc(f.properties.pop.toLocaleString()) + ' people (2020 Census)');
        }
      });
    }
    return L.geoJSON(data, {
      pointToLayer: function (f, ll) {
        return L.circleMarker(ll, { radius: key === 'homes' ? 6 : 5, color: '#111', weight: 1, fillColor: d.color, fillOpacity: 0.9 });
      },
      onEachFeature: function (f, l) { l.bindPopup(popupFor(key, f.properties), { maxWidth: 260 }); }
    });
  }

  function crimeColor(v, us) { if (!us) return '#868e96'; var r = v / us; return r < 0.75 ? '#2e9e5b' : r < 1.25 ? '#f59f00' : '#e03131'; }
  function rateLine(label, v, st, us) {
    if (v < 0) return '<tr><td>' + label + '</td><td colspan="3">Unavailable (add a free api.data.gov key in Settings)</td></tr>';
    return '<tr><td>' + label + '</td><td><b>' + esc(v.toFixed(1)) + '</b></td><td>' + esc(st > 0 ? st.toFixed(1) : '·') + '</td><td>' + esc(us > 0 ? us.toFixed(1) : '·') + '</td></tr>';
  }
  function loadCrime() {
    if (!center) return;
    var c = map.getCenter();
    say('Loading FBI crime data...');
    fetch('/map/crime?lat=' + c.lat.toFixed(4) + '&lon=' + c.lng.toFixed(4) + '&state=' + encodeURIComponent(center.state || ''))
      .then(function (r) { return r.json(); }).then(function (d) {
        if (d.error) { say(d.error); return; }
        var g = L.layerGroup(), used = {};
        (d.agencies || []).forEach(function (a) {
          // Agencies often share a courthouse address; stack them instead of hiding one.
          var k = a.Lat.toFixed(2) + ',' + a.Lon.toFixed(2);
          used[k] = (used[k] || 0) + 1;
          if (used[k] > 1) a.Lat -= 0.012 * (used[k] - 1);
          L.marker([a.Lat, a.Lon], { icon: L.divIcon({ className: 'crime-badge', iconSize: null,
              html: '<span style="background:' + crimeColor(a.Violent, a.USV) + '">' + esc(Math.round(a.Violent)) + '</span>' }),
              title: a.Name + ': ' + Math.round(a.Violent) + ' violent crimes per 100,000' })
            .bindPopup('<b>' + esc(a.Name) + '</b><br>' + esc(a.Year) + ', per 100,000 people' +
              (a.Population ? ' (serves about ' + esc(a.Population.toLocaleString()) + ')' : '') +
              '<table class="map-crime"><tr><th></th><th>Here</th><th>State</th><th>US</th></tr>' +
              rateLine('Violent', a.Violent, a.StateV, a.USV) + rateLine('Property', a.Property, a.StateP, a.USP) + '</table>' +
              '<small>Reported by the whole agency, not one neighborhood.</small>', { maxWidth: 300 })
            .addTo(g);
        });
        swap('crime', g);
        // The nearest agencies can sit just outside the view: widen it to show them.
        var inView = false;
        g.eachLayer(function (m) { if (map.getBounds().contains(m.getLatLng())) inView = true; });
        if (!inView && g.getLayers().length) map.fitBounds(L.featureGroup(g.getLayers()).getBounds().pad(0.15));
        say((d.agencies || []).length ? '' : 'No FBI crime reports for agencies near here.');
      }).catch(function () { say('FBI crime data could not be loaded.'); });
  }

  function floodHint() {
    if (isOn('flood') && map.getZoom() < 14) say('Zoom in to street level to see flood zones; FEMA only draws them that close.');
  }
  map.on('zoomend', floodHint);

  function swap(key, layer) {
    if (layers[key]) map.removeLayer(layers[key]);
    layers[key] = layer;
    if (isOn(key)) layer.addTo(map);
  }
  function isOn(key) { var cb = document.querySelector('[data-layer="' + key + '"]'); return cb && cb.checked; }

  function load(key) {
    var d = defs[key];
    if (d.tile) {
      // FEMA draws flood zones only at street level (1:36,000, zoom 14).
      swap(key, new Flood('', { pane: 'flood', opacity: 0.65, minZoom: 14, attribution: 'Flood zones: FEMA NFHL' }));
      floodHint();
      return;
    }
    if (d.crime) { loadCrime(); return; }
    if (d.kind === 'once' && loaded[key]) { if (layers[key]) layers[key].addTo(map); return; }
    if (d.kind === 'bbox' && span() > d.max) { say('Zoom in to see ' + d.label.toLowerCase() + '.'); if (layers[key]) map.removeLayer(layers[key]); return; }
    var b = bboxStr();
    if (d.kind === 'bbox' && loaded[key] === b) return;
    say('Loading ' + d.label.toLowerCase() + '...');
    fetch(d.url(b)).then(function (r) { return r.json(); }).then(function (data) {
      if (data.error) { say(d.label + ': ' + data.error); return; }
      loaded[key] = d.kind === 'bbox' ? b : true;
      swap(key, drawGeo(key, data));
      say(key === 'homes' && !data.features.length ? 'No listings with locations yet. Run a search above.' : '');
    }).catch(function () { say(d.label + ' could not be loaded.'); });
  }

  // Build the layer switches.
  // ?layers=flood,schools switches layers on at load, so a view can be bookmarked.
  var want = (new URLSearchParams(location.search).get('layers') || 'homes').split(',');
  var box = document.getElementById('map-layers');
  Object.keys(defs).forEach(function (key) {
    var d = defs[key];
    var lab = document.createElement('label'); lab.className = 'map-chip';
    var cb = document.createElement('input'); cb.type = 'checkbox'; cb.dataset.layer = key; cb.checked = want.indexOf(key) >= 0;
    var dot = document.createElement('span'); dot.className = 'map-dot'; dot.style.background = d.color || (d.tile ? '#4dabf7' : d.poly ? '#2171b5' : '#f59f00');
    lab.appendChild(cb); lab.appendChild(dot); lab.appendChild(document.createTextNode(d.label));
    box.appendChild(lab);
    cb.addEventListener('change', function () {
      var leg = document.querySelector('[data-legend="' + key + '"]'); if (leg) leg.hidden = !cb.checked;
      if (cb.checked) load(key); else if (layers[key]) map.removeLayer(layers[key]);
    });
  });

  var t = null;
  map.on('moveend', function () {
    clearTimeout(t);
    t = setTimeout(function () {
      Object.keys(defs).forEach(function (key) { if (isOn(key) && defs[key].kind === 'bbox') load(key); });
    }, 450);
  });

  fetch('/map/center').then(function (r) { return r.json(); }).then(function (c) {
    if (!c.ok) { map.setView([39.8, -98.6], 4); say('Enter a ZIP code above to center the map on it.'); return; }
    center = c;
    map.setView([c.lat, c.lon], want.length > 1 ? 13 : 12);
    want.forEach(function (k) {
      if (!defs[k]) return;
      var leg = document.querySelector('[data-legend="' + k + '"]'); if (leg) leg.hidden = false;
      load(k);
    });
  }).catch(function () { map.setView([39.8, -98.6], 4); });
})();
