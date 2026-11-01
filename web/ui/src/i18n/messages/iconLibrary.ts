export default {
  namespace: 'iconLibrary',
  messages: {
    'zh-CN': {
      title: '图标管理', description: '统一管理快捷资料、开场卡片和 Agent 头像的可选图标。隐藏与归档只影响后续选择，已保存的卡片和历史仍保留原图标。',
      module_shortcuts:'快捷资料', module_starters:'开场卡片', module_agents:'Agent 头像', module:'使用位置',
      readonly:'你可以查看并使用图标。图标库由管理员统一管理。', generate:'AI 生成图标', aiTitle:'描述你想要的图形',
      aiHint:'AI 生成轻量矢量图形，无需视觉模型。结果先预览，加入草稿后点击“保存图标库”才会发布。', promptPlaceholder:'例如：数据库连接，简洁的圆柱体与连接节点，适合小尺寸图标', promptLabel:'图标描述', model:'生成模型', count:'候选数量',
      generating:'正在生成候选图标…', previewOnly:'候选尚未保存，可取消或选择需要的图标。', addSelected:'加入草稿', added:'已加入草稿，请保存图标库后使用。',
      search:'搜索图标名称', all:'全部', custom:'自定义图标', builtin:'内置图标', archived:'已归档',
      builtinHint:'点击切换是否在当前模块的选择器中显示', hidden:'已隐藏', visible:'可选择', edit:'编辑', restore:'恢复', archive:'归档',
      empty:'还没有自定义图标。可以根据用途让 AI 生成。', noArchived:'没有已归档的图标。', noModules:'未分配位置',
      unsaved:'更改尚未保存', currentSaved:'图标库已同步', save:'保存图标库', saved:'图标库已保存，各模块选择器会同步更新。', reset:'撤销更改', loadLatest:'加载最新版本',
      conflict:'图标库已被其他人更新。你的草稿已保留，请记录需要保留的更改后点击“加载最新版本”重新编辑；不会自动覆盖新版本。',
      retry:'重试', loading:'正在加载图标库…', close:'关闭', cancel:'取消', name:'图标名称', editHint:'图形保存后固定不变。调整名称与使用位置不会改变历史图标。', apply:'应用到草稿',
    },
    en: {
      title:'Icon library', description:'Manage the icons available to reference shortcuts, starter cards and Agent avatars. Hiding or archiving changes future choices; saved cards and history keep their icons.',
      module_shortcuts:'References', module_starters:'Starter cards', module_agents:'Agent avatars', module:'Used in', readonly:'You can view and use icons. Administrators manage the shared library.',
      generate:'Generate icons', aiTitle:'Describe an icon', aiHint:'AI generates lightweight vector shapes with a text model. Preview the results, add selected icons to the draft, then save the library to publish.',
      promptPlaceholder:'For example: a database cylinder with a small connection node, clear at small sizes', promptLabel:'Icon description', model:'Model', count:'Candidate count', generating:'Generating candidates…', previewOnly:'These candidates are not saved. Select the ones to keep or cancel.', addSelected:'Add to draft', added:'Added to draft. Save the library to use these icons.',
      search:'Search icon names', all:'All', custom:'Custom icons', builtin:'Built-in icons', archived:'Archived', builtinHint:'Click to show or hide in this module’s picker', hidden:'Hidden', visible:'Available', edit:'Edit', restore:'Restore', archive:'Archive', empty:'No custom icons yet. Describe one to generate it with AI.', noArchived:'No archived icons.', noModules:'No modules assigned',
      unsaved:'Unsaved changes', currentSaved:'Library is up to date', save:'Save library', saved:'Icon library saved. Pickers have been updated.', reset:'Discard changes', loadLatest:'Load latest version', conflict:'Someone else updated the library. Your draft is retained. Copy any changes you want to keep, then load the latest version and edit again. The newer version will not be overwritten.',
      retry:'Retry', loading:'Loading icons…', close:'Close', cancel:'Cancel', name:'Icon name', editHint:'Saved shapes are fixed. Renaming or changing usage does not alter historical icons.', apply:'Apply to draft',
    },
  },
};
