Pod::Spec.new do |s|
  s.name           = 'GedaTransfer'
  s.version        = '1.0.0'
  s.summary        = 'A sample project summary'
  s.description    = 'A sample project description'
  s.author         = ''
  s.homepage       = 'https://docs.expo.dev/modules/'
  # iOS only. ActivityKit, BackgroundTasks with external-power requirements,
  # and the photo library this module exists to move all stop at the iPhone.
  s.platforms      = {
    :ios => '16.4'
  }
  s.source         = { git: '' }
  s.static_framework = true

  s.dependency 'ExpoModulesCore'

  # Swift/Objective-C compatibility
  s.pod_target_xcconfig = {
    'DEFINES_MODULE' => 'YES',
  }

  # Not recursive: `checks/` holds a command-line pin checker with its own
  # `main.swift`, run by scripts/verify-p4.sh. Compiling it into the app is
  # both wrong and a build error.
  s.source_files = "*.{h,m,mm,swift,hpp,cpp}"
end
