//! Native sleep/wake signals distinguish resume from an ordinary delayed timer.
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};

static ASLEEP: AtomicBool = AtomicBool::new(false);
static RESUMED_AT: AtomicI64 = AtomicI64::new(0);

pub fn state() -> (bool, i64) {
    (
        ASLEEP.load(Ordering::SeqCst),
        RESUMED_AT.load(Ordering::SeqCst),
    )
}

fn resume() {
    RESUMED_AT.store(chrono::Utc::now().timestamp_millis(), Ordering::SeqCst);
    ASLEEP.store(false, Ordering::SeqCst);
}

#[cfg(target_os = "macos")]
pub fn install() -> Result<(), String> {
    use block2::RcBlock;
    use objc2_app_kit::{
        NSWorkspace, NSWorkspaceDidWakeNotification, NSWorkspaceWillSleepNotification,
    };
    use objc2_foundation::NSNotification;
    let center = NSWorkspace::sharedWorkspace().notificationCenter();
    let sleep =
        RcBlock::new(|_: std::ptr::NonNull<NSNotification>| ASLEEP.store(true, Ordering::SeqCst));
    let wake = RcBlock::new(|_: std::ptr::NonNull<NSNotification>| resume());
    // The notification center retains the observer tokens and copied blocks for
    // the process lifetime. Neither callback touches UI or account credentials.
    unsafe {
        center.addObserverForName_object_queue_usingBlock(
            Some(NSWorkspaceWillSleepNotification),
            None,
            None,
            &sleep,
        );
        center.addObserverForName_object_queue_usingBlock(
            Some(NSWorkspaceDidWakeNotification),
            None,
            None,
            &wake,
        );
    }
    Ok(())
}

#[cfg(target_os = "windows")]
pub fn install() -> Result<(), String> {
    use std::ffi::c_void;
    #[repr(C)]
    struct Subscription {
        callback: unsafe extern "system" fn(*mut c_void, u32, *mut c_void) -> u32,
        context: *mut c_void,
    }
    #[link(name = "PowrProf")]
    extern "system" {
        fn PowerRegisterSuspendResumeNotification(
            flags: u32,
            recipient: *const Subscription,
            handle: *mut *mut c_void,
        ) -> u32;
    }
    unsafe extern "system" fn callback(_: *mut c_void, event: u32, _: *mut c_void) -> u32 {
        match event {
            4 => ASLEEP.store(true, Ordering::SeqCst), // PBT_APMSUSPEND
            7 | 18 => resume(),                        // PBT_APMRESUMESUSPEND / AUTOMATIC
            _ => {}
        }
        0
    }
    let subscription = Box::new(Subscription {
        callback,
        context: std::ptr::null_mut(),
    });
    let mut handle = std::ptr::null_mut();
    // Windows retains this callback until process exit; context is never used.
    let result = unsafe { PowerRegisterSuspendResumeNotification(2, &*subscription, &mut handle) };
    if result == 0 {
        // Keep the subscription address valid for the registered callback's lifetime.
        let _ = Box::into_raw(subscription);
        Ok(())
    } else {
        Err("无法注册系统休眠通知".into())
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
pub fn install() -> Result<(), String> {
    Ok(())
}
