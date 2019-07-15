package tpm2

func (t *tpmImpl) Create(parentHandle ResourceContext, inSensitive *SensitiveCreate, inPublic *Public,
	outsideInfo Data, creationPCR PCRSelectionList, session interface{}) (Private, *Public, *CreationData,
	Digest, *TkCreation, error) {
	if parentHandle == nil {
		return nil, nil, nil, nil, nil, InvalidParamError{"nil parentHandle"}
	}
	if inSensitive == nil {
		inSensitive = &SensitiveCreate{}
	}
	if inPublic == nil {
		return nil, nil, nil, nil, nil, InvalidParamError{"nil inPublic"}
	}
	if err := t.checkResourceContextParam(parentHandle); err != nil {
		return nil, nil, nil, nil, nil, err
	}

	var outPrivate Private
	var outPublic Public
	var creationData CreationData
	var creationHash Digest
	var creationTicket TkCreation

	if err := t.RunCommand(CommandCreate, Format{1, 4}, Format{0, 5}, parentHandle.Handle(), inSensitive,
		inPublic, outsideInfo, creationPCR, &outPrivate, &outPublic, &creationData, &creationHash,
		&creationTicket, session); err != nil {
		return nil, nil, nil, nil, nil, err
	}

	return outPrivate, &outPublic, &creationData, creationHash, &creationTicket, nil
}

func (t *tpmImpl) Load(parentHandle ResourceContext, inPrivate Private, inPublic *Public,
	session interface{}) (ResourceContext, Name, error) {
	if parentHandle == nil {
		return nil, nil, InvalidParamError{"nil parentHandle"}
	}
	if inPublic == nil {
		return nil, nil, InvalidParamError{"nil inPublic"}
	}
	if err := t.checkResourceContextParam(parentHandle); err != nil {
		return nil, nil, err
	}

	pubCopy := inPublic.Copy()
	if pubCopy == nil {
		return nil, nil, InvalidParamError{"inPublic couldn't be copied"}
	}

	var objectHandle Handle
	var name Name

	if err := t.RunCommand(CommandLoad, Format{1, 2}, Format{1, 1}, parentHandle.Handle(), inPrivate,
		inPublic, &objectHandle, &name, session); err != nil {
		return nil, nil, err
	}

	objectHandleRc := &objectContext{handle: objectHandle, public: *pubCopy, name: name}
	t.addResourceContext(objectHandleRc)

	return objectHandleRc, name, nil
}

func (t *tpmImpl) LoadExternal(inPrivate *Sensitive, inPublic *Public, hierarchy Handle) (ResourceContext, Name,
	error) {
	if inPublic == nil {
		return nil, nil, InvalidParamError{"nil inPublic"}
	}

	pubCopy := inPublic.Copy()
	if pubCopy == nil {
		return nil, nil, InvalidParamError{"inPublic couldn't be copied"}
	}

	var objectHandle Handle
	var name Name

	if err := t.RunCommand(CommandLoadExternal, Format{0, 3}, Format{1, 1}, inPrivate, inPublic,
		hierarchy, &objectHandle, &name); err != nil {
		return nil, nil, err
	}

	objectHandleRc := &objectContext{handle: objectHandle, public: *pubCopy, name: name}
	t.addResourceContext(objectHandleRc)

	return objectHandleRc, name, nil
}

func (t *tpmImpl) readPublic(objectHandle Handle) (*Public, Name, Name, error) {
	var outPublic Public
	var name Name
	var qualifiedName Name
	if err := t.RunCommand(CommandReadPublic, Format{1, 0}, Format{0, 3}, objectHandle, &outPublic, &name,
		&qualifiedName); err != nil {
		return nil, nil, nil, err
	}
	return &outPublic, name, qualifiedName, nil
}

func (t *tpmImpl) ReadPublic(objectHandle ResourceContext) (*Public, Name, Name, error) {
	if objectHandle == nil {
		return nil, nil, nil, InvalidParamError{"nil objectHandle"}
	}
	if err := t.checkResourceContextParam(objectHandle); err != nil {
		return nil, nil, nil, err
	}
	return t.readPublic(objectHandle.Handle())
}
