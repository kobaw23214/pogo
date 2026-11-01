// Pogo managed-browser additions to pixelflux; MPL-2.0.
// The marker is checked against the actual software render buffer before encoding.
use std::{sync::{mpsc::Sender, atomic::Ordering}, time::{Duration, Instant}};
use serde_json::{Value, json};
use smithay::wayland::seat::WaylandFocus;
use smithay::{desktop::Window, utils::SERIAL_COUNTER, wayland::{compositor::with_states, shell::xdg::{ShellClient, XdgToplevelSurfaceData}}};
use crate::wayland::frontend::{AppState, FocusTarget, window_meta, window_output_id, PARKED_LOGICAL_SIZE};

pub fn manual() -> bool { std::env::var("PIXELFLUX_MANUAL_PLACEMENT").as_deref() == Ok("1") }
pub fn park_size(w: &Window) -> (i32,i32) {
    if manual() { let id = window_meta(w).map(|m| m.id).unwrap_or(0) as i32; (320 + (id % 512)*2, 240 + (id / 512)*2) } else { PARKED_LOGICAL_SIZE }
}
pub struct Ping { pub client: ShellClient, pub reply: Sender<Value>, deadline: Instant }
struct Probe { output:u32, bits:u64, x:usize, y:usize, absent:bool, min:u64, reply:Sender<Value>, deadline:Instant }
#[derive(Default)]
pub struct State { pub pings: Vec<Ping>, probes: Vec<Probe>, last_background:Option<Instant> }
fn err(reply: Sender<Value>, text:&str) { let _ = reply.send(json!({"error":text})); }
fn target_id(state:&AppState,target:Option<FocusTarget>)->Option<u32> {
    match target {
        Some(FocusTarget::Window(w)) => window_meta(&w).map(|m|m.id),
        Some(FocusTarget::Popup(kind)) => smithay::desktop::find_popup_root_surface(&kind).ok().and_then(|root| state.space.elements().find(|w|w.wl_surface().as_deref()==Some(&root)).and_then(window_meta).map(|m|m.id)),
        _ => None
    }
}
fn number(v:&Value,k:&str)->Option<u32> { v.get(k)?.as_u64().and_then(|n| u32::try_from(n).ok()) }
fn window(state:&AppState,id:u32)->Option<Window> { state.space.elements().chain(state.pending_windows.iter()).find(|w|window_meta(w).is_some_and(|m|m.id==id)).cloned() }
pub fn content_seq(state:&AppState,id:u32)->u64 {
    state.space.elements().filter(|w|window_output_id(w)==id && !window_meta(w).is_some_and(|m|m.parked.load(Ordering::Relaxed)))
        .filter_map(window_meta).map(|m|m.content_seq.load(Ordering::Relaxed)).max().unwrap_or(0)
}
pub fn command(state:&mut AppState,v:Value,reply:Sender<Value>) {
    if !manual() { return err(reply,"Pogo manual placement is disabled"); }
    match v["op"].as_str().unwrap_or("") {
        "windows" => {
            let rows:Vec<Value> = state.space.elements().chain(state.pending_windows.iter()).filter_map(|w| {
                let m=window_meta(w)?; let g=w.geometry();let outer=w.bbox();
                let (title, app_id) = w.toplevel().map(|t| with_states(t.wl_surface(), |states| { let a = states.data_map.get::<XdgToplevelSurfaceData>().unwrap().lock().unwrap(); (a.title.clone(),a.app_id.clone()) })).unwrap_or_default();
                Some(json!({"window_id":m.id,"title":title.unwrap_or_default(),"app_id":app_id.unwrap_or_default(),"output_id":m.output.load(Ordering::Relaxed),"parked":m.parked.load(Ordering::Relaxed),"width":g.size.w,"height":g.size.h,"outer_width":outer.size.w,"outer_height":outer.size.h,"content_seq":m.content_seq.load(Ordering::Relaxed)}))
            }).collect(); let _=reply.send(json!(rows));
        },
        "pointer_target" => {
            let Some(pointer)=state.seat.get_pointer() else {return err(reply,"No pointer")};
            let focus=target_id(state,pointer.current_focus());
            let grab=pointer.grab_start_data().and_then(|g|g.focus).and_then(|(f,_)|target_id(state,Some(f)));
            let _=reply.send(json!({"window_id":focus,"grab_window_id":grab}));
        },
        "focus" => {
            let Some(w)=number(&v,"window_id").and_then(|id|window(state,id)) else {return err(reply,"Unknown window")};
            if window_meta(&w).is_some_and(|m|m.parked.load(Ordering::Relaxed)) {return err(reply,"Window is parked")};
            if let Some(k)=state.seat.get_keyboard() { k.set_focus(state,Some(FocusTarget::Window(w)),SERIAL_COUNTER.next_serial()); }
            let _=reply.send(json!({"ack":true}));
        },
        "modifiers" => {
            if let Some(k)=state.seat.get_keyboard() {
                let mut m=k.modifier_state(); m.ctrl=v["ctrl"].as_bool().unwrap_or(false);m.shift=v["shift"].as_bool().unwrap_or(false);m.alt=v["alt"].as_bool().unwrap_or(false);m.logo=v["logo"].as_bool().unwrap_or(false);m.caps_lock=false;m.num_lock=false;
                k.set_modifier_state(m);k.advertise_modifier_state(state);
            } let _=reply.send(json!({"ack":true}));
        },
        "release" => {
            let Some(w)=number(&v,"window_id").and_then(|id|window(state,id)) else {let _=reply.send(json!({"ack":true}));return};
            if let Some(k)=state.seat.get_keyboard() {
                if target_id(state,k.current_focus())!=window_meta(&w).map(|m|m.id) {let _=reply.send(json!({"ack":true}));return}
                let mut m=k.modifier_state();m.ctrl=false;m.shift=false;m.alt=false;m.logo=false;k.set_modifier_state(m);k.advertise_modifier_state(state);k.set_focus(state,None,SERIAL_COUNTER.next_serial());}
            let _=reply.send(json!({"ack":true}));
        },
        "park" => {
            let Some(w)=number(&v,"window_id").and_then(|id|window(state,id)) else {return err(reply,"Unknown window")};
            state.park_window(&w,window_output_id(&w));let _=reply.send(json!({"ack":true}));
        },
        "park_if_present" => {
            let Some(id)=number(&v,"window_id") else {return err(reply,"window_id required")};
            // Cleanup after Chromium Page.close must test and park on this same
            // compositor dispatch. A separate windows() followed by park() races
            // the client's destroy event and must never mask unrelated failures.
            if let Some(w)=window(state,id) {
                state.park_window(&w,window_output_id(&w));
                let _=reply.send(json!({"ack":true,"present":true}));
            } else {let _=reply.send(json!({"ack":true,"present":false}));}
        },
        "sync" => {
            let Some(w)=number(&v,"window_id").and_then(|id|window(state,id)) else {return err(reply,"Unknown window")};
            let Some(client)=w.toplevel().map(|t|t.client()) else {return err(reply,"Window has no client")};
            if state.pogo.pings.iter().any(|p|p.client==client) {return err(reply,"Client sync already pending")};
            if client.send_ping(SERIAL_COUNTER.next_serial()).is_err() {return err(reply,"Client ping rejected")};
            state.pogo.pings.push(Ping{client,reply,deadline:Instant::now()+Duration::from_secs(5)});
        },
        "probe_present" | "probe_absent" => {
            let Some(output)=number(&v,"output_id") else {return err(reply,"output_id required")};
            if !state.output_nodes.iter().any(|n|n.id==output && n.capture.is_some()) {return err(reply,"Output must be capturing for a pixel fence")};
            let Some(raw)=v["nonce"].as_str().filter(|s|s.len()==16) else {return err(reply,"nonce must be 16 hex characters")};
            let Ok(bits)=u64::from_str_radix(raw,16) else {return err(reply,"Invalid nonce")};
            let x=number(&v,"pixel_x").unwrap_or(2) as usize;let y=number(&v,"pixel_y").unwrap_or(2) as usize;
            if x>3840 || y>2400 {return err(reply,"Probe location is out of bounds")};
            if state.pogo.probes.iter().any(|p|p.output==output) {return err(reply,"Output probe already pending")};
            state.pogo.probes.push(Probe{output,bits,x,y,absent:v["op"]=="probe_absent",min:v["min_seq"].as_u64().unwrap_or(0),reply,deadline:Instant::now()+Duration::from_secs(5)});
            if let Some(cap)=state.output_nodes.iter_mut().find(|n|n.id==output).and_then(|n|n.capture.as_mut()) {cap.request_idr();}
        },
        _ => err(reply,"Unknown Pogo native command")
    }
}
pub fn observe(state:&mut AppState,id:u32,width:i32,height:i32,buf:&[u8],seq:u64) {
    let Some(i)=state.pogo.probes.iter().position(|p|p.output==id) else {return};
    let p=&state.pogo.probes[i];
    if p.x+16>width as usize || p.y+16>height as usize || buf.len()<(width as usize)*(height as usize)*4 {return};
    let mut matches=true;
    for bit in 0..64 { let off=((p.y+(bit/8)*2+1)*width as usize+p.x+(bit%8)*2+1)*4;let white=p.bits&(1u64<<(63-bit))!=0;
        if !(0..3).all(|c|if white {buf[off+c]>=224} else {buf[off+c]<=31}) {matches=false;break}
    }
    let passed=if p.absent {seq>p.min && !matches} else {matches};
    if passed {let p=state.pogo.probes.swap_remove(i);let _=p.reply.send(json!({"content_seq":seq,"observed":if p.absent {"absent"} else {"present"}}));}
}
pub fn tick(state:&mut AppState) {
    let now=Instant::now();let mut i=0;
    while i<state.pogo.probes.len() {if state.pogo.probes[i].deadline<=now {let p=state.pogo.probes.swap_remove(i);err(p.reply,"Causal pixel fence timed out")} else {i+=1}}
    let mut i=0;while i<state.pogo.pings.len() {if state.pogo.pings[i].deadline<=now {let p=state.pogo.pings.swap_remove(i);err(p.reply,"Wayland client sync timed out")} else {i+=1}}
}
pub fn background_callbacks(state:&mut AppState) {
    if state.pogo.last_background.is_some_and(|t|t.elapsed()<Duration::from_millis(16)) {return}
    state.pogo.last_background=Some(Instant::now());
    let time=state.clock.now(); let Some(primary)=state.primary_output().cloned() else {return};
    for w in state.space.elements() {
        let id=window_output_id(w);let parked=window_meta(w).is_some_and(|m|m.parked.load(Ordering::Relaxed));
        if parked || !state.output_nodes.iter().any(|n|n.id==id&&n.capture.is_some()) {
            let output=state.output_nodes.iter().find(|n|n.id==id).map(|n|n.output.clone()).unwrap_or_else(||primary.clone());
            w.send_frame(&output,time,Some(Duration::ZERO),|_,_|Some(output.clone()));
        }
    }
}
