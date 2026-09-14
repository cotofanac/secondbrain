// Permission is requested synchronously from the user's tap, after preparation.
let pushRegistration=null, pushKey=null, pushSubscription=null, pushRegistered=false, preparingPush=null, pushNeedsRenewal=false;
function pushSupported(){return isSecureContext && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;}
function urlBase64ToUint8Array(value){const raw=atob((value+'='.repeat((4-value.length%4)%4)).replace(/-/g,'+').replace(/_/g,'/'));return Uint8Array.from(raw,c=>c.charCodeAt(0));}
async function isBraveBrowser(){
 try{return !!navigator.brave && await navigator.brave.isBrave();}catch(_){return false;}
}
async function pushSetupError(error){
 const message=error?.message || 'Could not enable this device. Try again.';
 if(await isBraveBrowser() && /registration failed|push service error/i.test(message)){
  return 'Brave’s push service is off. Open brave://settings/privacy, enable “Use Google services for push messaging,” relaunch Brave, then try again.';
 }
 return message;
}
async function pushAPI(path,body){
 const controller=new AbortController();const timeout=setTimeout(()=>controller.abort(),20000);
 try{const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined,signal:controller.signal});
 if(response.status===401||response.redirected)throw new Error('Session expired. Sign in again, then enable this device.');
 const data=await response.json();if(!response.ok){const error=new Error(data.error || 'Could not update notifications.');error.renew=data.renew===true;throw error;}return data;
 }finally{clearTimeout(timeout);}
}
function displayPush(message,last){
 const status=document.getElementById('push-status');if(status)status.textContent=message;
 const result=document.getElementById('push-last-result');if(result)result.textContent=last || '';
 const enable=document.getElementById('push-enable'),disable=document.getElementById('push-disable'),test=document.getElementById('push-test');
 if(enable){enable.disabled=!pushRegistration||!pushKey;enable.hidden=pushRegistered;}
 if(disable)disable.hidden=!pushSubscription;
 if(test)test.hidden=!pushRegistered;
}
function waitForWorker(reg){return new Promise((resolve,reject)=>{if(reg.active){resolve(reg);return;}const timer=setTimeout(()=>reject(new Error('App setup timed out. Close and reopen the app, then try again.')),15000);const check=()=>{if(reg.active){clearTimeout(timer);resolve(reg);}};reg.addEventListener('updatefound',()=>reg.installing?.addEventListener('statechange',check));reg.installing?.addEventListener('statechange',check);reg.waiting?.addEventListener('statechange',check);});}
async function preparePush(){
 if(preparingPush)return preparingPush;
 if(!pushSupported()){displayPush(!isSecureContext?'Notifications need an HTTPS connection.':'Open the installed Home Screen app to enable notifications on iPhone.');return;}
 preparingPush=(async()=>{try{
  displayPush('Checking this device…');
  pushRegistration=await waitForWorker(await navigator.serviceWorker.register('/static/sw.js',{scope:'/',updateViaCache:'none'}));
  pushKey=(await pushAPI('/push/public-key')).key;
  pushSubscription=await pushRegistration.pushManager.getSubscription();pushRegistered=false;
  if(Notification.permission==='denied'){displayPush('Permission blocked. Allow SecondBrain in your device’s notification settings.');return;}
  if(pushSubscription){
   const currentKey=pushSubscription.options.applicationServerKey;const expected=urlBase64ToUint8Array(pushKey);
   if(currentKey&&(new Uint8Array(currentKey).length!==expected.length||new Uint8Array(currentKey).some((v,i)=>v!==expected[i]))){pushNeedsRenewal=true;displayPush('This device needs a new subscription. Disable it, then enable it again.');return;}
   // Upsert preserves per-device delivery history; repairs a missing server row.
   await pushAPI('/push/subscribe',pushSubscription.toJSON());
   const status=await pushAPI('/push/status',{endpoint:pushSubscription.endpoint});pushRegistered=status.registered;
   displayPush(pushRegistered?'This device is enabled. Permission granted.':'Subscription needs repair.',status.last_result);
   if(pushRegistered)await retireOldWorker();
  }else displayPush(Notification.permission==='granted'?'Permission granted. Enable this device to finish setup.':'Notifications are off on this device.');
 }catch(error){if(error.renew)pushNeedsRenewal=true;displayPush(error.name==='AbortError'?'The server did not respond. Try again.':error.message);}finally{preparingPush=null;}})();
 return preparingPush;
}
async function retireOldWorker(){
 // Do not lose a working old subscription until the root subscription is saved.
 for(const reg of await navigator.serviceWorker.getRegistrations()){
  if(reg.scope===new URL('/static/',location.origin).href){const old=await reg.pushManager.getSubscription();if(old){await pushAPI('/push/unsubscribe',{endpoint:old.endpoint});await old.unsubscribe();}await reg.unregister();}
 }
}
function enablePush(){
 if(!pushRegistration||!pushKey){preparePush();return;}
 // No await before this call: Safari requires direct user activation.
 const permission=Notification.permission==='granted'?Promise.resolve('granted'):Notification.requestPermission();
 const button=document.getElementById('push-enable');if(button)button.disabled=true;
 permission.then(async granted=>{if(granted!=='granted'){displayPush('Permission was not granted. Allow SecondBrain in notification settings.');return;}
 try{if(pushNeedsRenewal&&pushSubscription){await pushAPI('/push/unsubscribe',{endpoint:pushSubscription.endpoint});await pushSubscription.unsubscribe();pushSubscription=null;pushNeedsRenewal=false;}pushSubscription=await pushRegistration.pushManager.subscribe({userVisibleOnly:true,applicationServerKey:urlBase64ToUint8Array(pushKey)});await pushAPI('/push/subscribe',pushSubscription.toJSON());pushRegistered=true;displayPush('This device is enabled. Send a test to check delivery.');await retireOldWorker();}
 catch(error){pushRegistered=false;displayPush(await pushSetupError(error));}
 }).catch(error=>displayPush(error.message));
}
async function disablePush(){try{if(preparingPush)await preparingPush;if(pushSubscription){await pushAPI('/push/unsubscribe',{endpoint:pushSubscription.endpoint});pushRegistered=false;await pushSubscription.unsubscribe();pushSubscription=null;}displayPush('Notifications are off on this device.');}catch(error){displayPush(error.message);}}
async function testPush(){const button=document.getElementById('push-test');if(button)button.disabled=true;try{const result=await pushAPI('/push/test',{endpoint:pushSubscription.endpoint});if(result.registered===false){pushRegistered=false;pushNeedsRenewal=true;}displayPush('Test notification',result.message);}catch(error){displayPush(error.message);}finally{if(button)button.disabled=false;}}
// Register even on browsers without PushManager so asset updates still work.
document.addEventListener('DOMContentLoaded',()=>{if(pushSupported())preparePush();else if(isSecureContext&&'serviceWorker' in navigator)navigator.serviceWorker.register('/static/sw.js',{scope:'/',updateViaCache:'none'}).catch(()=>{});});
