"use strict";
(() => {
  const env = document.body.dataset.environment;
  const $ = id => document.getElementById(id);
  const jst = (value, date = false) => value ? new Intl.DateTimeFormat("ja-JP", {timeZone:"Asia/Tokyo", ...(date ? {year:"numeric",month:"2-digit",day:"2-digit"} : {hour:"2-digit",minute:"2-digit",second:"2-digit",hourCycle:"h23"})}).format(new Date(value)) : "—";
  const number = value => value == null ? "—" : new Intl.NumberFormat("ja-JP",{maximumFractionDigits:2}).format(value);
  const el = (tag, text, cls) => { const node = document.createElement(tag); if (text != null) node.textContent = text; if (cls) node.className = cls; return node; };
  let selected = "", busy = false;
  $("today").textContent = jst(new Date(),true);

  function graph(quote) {
    $("chart-name").textContent = quote ? quote.name : "保存データなし";
    $("chart-symbol").textContent = quote ? quote.symbol : "NO SAVED QUOTES";
    $("chart-price").textContent = quote ? number(quote.price) : "—";
    const points = quote ? quote.points : [];
    $("point-count").textContent = points.length + " points";
    $("chart-empty").hidden = points.length > 0;
    const group = $("chart-content"); group.replaceChildren();
    const svg = (tag, attrs, text) => { const n = document.createElementNS("http://www.w3.org/2000/svg", tag); Object.entries(attrs).forEach(([k,v]) => n.setAttribute(k,String(v))); if(text != null) n.textContent = text; group.append(n); return n; };
    if (!points.length) return;
    let min = Math.min(...points.map(p=>p.price)), max = Math.max(...points.map(p=>p.price));
    const padding = Math.max((max-min)*0.22, max*0.0005, 0.5); min-=padding;max+=padding;
    const start = new Date(points[0].time).getTime(), end = new Date(points[points.length-1].time).getTime();
    for (let i=0;i<4;i++) { const y=20+i*50; svg("line",{x1:12,y1:y,x2:680,y2:y,class:"gridline"});svg("text",{x:695,y:y+4},number(max-(max-min)*i/3)); }
    svg("text",{x:12,y:210},jst(points[0].time));
    if (points.length>1) svg("text",{x:680,y:210,"text-anchor":"end"},jst(points[points.length-1].time));
    points.forEach((p,i)=>{ const x=end===start ? 345 : 12+668*(new Date(p.time).getTime()-start)/(end-start), y=20+150*(max-p.price)/(max-min); const dot=svg("circle",{cx:x,cy:y,r:i===points.length-1?4:2.8,class:i===points.length-1?"last-point":"chart-point"}); const title=document.createElementNS("http://www.w3.org/2000/svg","title");title.textContent=jst(p.time)+" / "+number(p.price)+"円";dot.append(title); });
  }

  function render(view) {
    const runtime=view.runtime||{}, active=runtime.state==="active";
    const phaseNames={starting:"起動中",credentials:"認証情報の準備中",authenticating:"認証中",collecting:"取得中",waiting:"次回取得の待機中"};
    const stateNames={completed:"収集完了",scheduled_end:"終了時刻に到達",interrupted:"中断",failed:"エラーで停止",stale:"稼働未確認",unrecorded:"記録なし",unavailable:"状態を読み取れません",clock_error:"時刻を確認"};
    $("runtime-state").textContent=active ? phaseNames[runtime.phase]||"状態不明" : stateNames[runtime.state]||"記録なし";
    $("runtime-indicator").className="runtime-indicator"+(active?" active":["failed","stale","unavailable","clock_error"].includes(runtime.state)?" warning":"");
    $("runtime-progress").textContent=runtime.planned ? "今回の保存 "+number(runtime.saved)+" 件 / 上限 "+number(runtime.planned)+" 件" : "この保存先の収集記録";
    $("runtime-updated").textContent=runtime.updated_at ? "状態更新 "+jst(runtime.updated_at,true)+" "+jst(runtime.updated_at)+" JST" : "更新版の収集プログラムを起動すると表示されます";
    $("latest-time").replaceChildren(document.createTextNode(jst(view.latest_at)),el("small","JST"));
    $("latest-date").textContent = view.latest_at ? jst(view.latest_at,true)+" / 取得完了" : "まだ取得データがありません";
    $("file-count").textContent = number(view.files);
    $("file-caption").textContent = "全保存期間 / "+number(view.bytes/1024/1024)+" MiB";
    $("incomplete-count").textContent = number(view.incomplete_files);
    const q=view.quality;
    $("api-error-count").textContent = q.available ? number(q.api_errors) : "—";
    $("quality-status").textContent = !q.available ? "データなし" : q.needs_review ? "要確認" : "指摘なし";
    $("quality-status").className = "quality-status" + (!q.available ? " unavailable" : q.needs_review ? " review" : "");
    [["invalid-count",q.invalid_files],["missing-price",q.missing_prices],["missing-time",q.missing_times]].forEach(([id,value])=>{ $(id).textContent=q.available ? number(value)+" 件" : "—";$(id).classList.toggle("warning",q.available && value>0); });
    $("quality-detail").textContent=env==="test" ? "検証APIのダミー応答では、価格や時刻の欠損が想定されます。" : "時刻異常なども含む集計です。市場時間や鮮度の合否は判定していません。";
    const rows=$("quote-rows");rows.replaceChildren();
    $("quotes-empty").hidden=view.quotes.length>0;
    $("quote-count").textContent=view.quotes.length+" 銘柄";
    view.quotes.forEach(quote=>{
      const row=el("tr"),name=el("td"),button=el("button",quote.name,"symbol-button");button.type="button";button.setAttribute("aria-label",quote.name+"の価格記録を表示");button.addEventListener("click",()=>{selected=quote.symbol;graph(quote);});name.append(button,el("span",quote.symbol,"symbol-code"));row.append(name,el("td",quote.price==null?"—":number(quote.price)+" 円","number"));
      [quote.market_time,quote.received_at].forEach(time=>{ const cell=el("td",jst(time));if(time) cell.append(el("span",jst(time,true),"symbol-code"));row.append(cell); });rows.append(row);
    });
    graph(view.quotes.find(quote=>quote.symbol===selected)||view.quotes[0]);
    const list=$("recent-list");list.replaceChildren();$("recent-empty").hidden=view.recent.length>0;
    view.recent.forEach(run=>{const li=el("li",null,run.complete?"":"partial"),detail=el("div",run.complete?"取得完了":"部分保存");detail.append(el("small",run.records+" records / "+jst(run.finished_at,true)));const time=el("time",jst(run.finished_at));time.dateTime=run.finished_at;li.append(detail,time);list.append(li);});
    $("updated-at").textContent="表示データ作成 "+jst(view.updated_at)+" JST / 10秒ごとに更新";
  }

  async function refresh() {
    if(busy) return;busy=true;$("refresh").disabled=true;
    try { const response=await fetch("/api/status?environment="+encodeURIComponent(env),{cache:"no-store",credentials:"omit"});if(!response.ok) throw new Error("display unavailable");render(await response.json());$("connection-error").hidden=true; }
    catch { $("connection-error").hidden=false; }
    finally { busy=false;$("refresh").disabled=false; }
  }
  $("refresh").addEventListener("click",refresh);
  document.addEventListener("visibilitychange",()=>{if(!document.hidden) refresh();});
  setInterval(()=>{if(!document.hidden) refresh();},10000);
  refresh();
})();
